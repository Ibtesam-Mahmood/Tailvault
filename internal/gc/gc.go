// Package gc implements the retention half of tailvault's anti-bloat promise:
// unreferenced blobs on the storage node are pruned by an explicit, per-branch
// mark-and-sweep (SPEC Q4: mark on push, sweep on explicit `tailvault gc`).
//
// The keep-set is the UNION of every local branch's committed tailvault.lock —
// each branch tip contributes its entries' sha256 plus, for history-on entries,
// every sha in versions[]. A blob in objects/<sha> that is in no branch's
// keep-set and is not marked preserve is eligible for deletion. This makes
// "a delete on branch A must not nuke a blob branch B still references" fall out
// naturally: branch B's lock keeps the sha in the union.
//
// This file currently provides the pure decision core (KeepSet, Plan,
// PlanSweep, StripObjectKey) which has no data-layer dependency. BuildKeepSet
// (reads parsed per-branch locks) and Sweep (calls Backend.Delete) land once the
// lock and backend packages are merged at M1.
package gc

import "strings"

// objectPrefix is the storage prefix for content-addressed blobs.
const objectPrefix = "objects/"

// KeepSet is a set of bare shas that must survive a sweep — the union of shas
// referenced by any local branch's committed lock (current sha256 + history
// versions[]). It is also used to carry the set of preserve-protected shas.
type KeepSet map[string]struct{}

// Add inserts a bare sha into the set.
func (k KeepSet) Add(sha string) { k[sha] = struct{}{} }

// Has reports whether a bare sha is in the set.
func (k KeepSet) Has(sha string) bool { _, ok := k[sha]; return ok }

// Plan is the result of the mark step: which blobs would be deleted vs kept.
type Plan struct {
	Eligible  []string // bare shas with no branch ref and no preserve — the sweep targets
	Kept      int      // blobs surviving because a branch lock references them
	Preserved int      // blobs surviving only because they are marked preserve
}

// PlanSweep classifies every stored blob against the keep-set and the
// preserve-set. stored is the raw key list from Backend.List("objects/"); keys
// are stripped to bare sha for comparison. A blob survives if its sha is in the
// keep-set (Kept) or is preserve-protected (Preserved); otherwise it is
// Eligible for deletion. keep is checked first so a blob both referenced and
// preserved counts as Kept, never double-counted.
//
// Eligible preserves the input order of stored for stable, testable output.
func PlanSweep(stored []string, keep, preserveShas KeepSet) Plan {
	var p Plan
	for _, key := range stored {
		sha := StripObjectKey(key)
		switch {
		case keep.Has(sha):
			p.Kept++
		case preserveShas.Has(sha):
			p.Preserved++
		default:
			p.Eligible = append(p.Eligible, sha)
		}
	}
	return p
}

// StripObjectKey reduces a stored key to its bare sha. "objects/<sha>" -> "<sha>";
// a key without the prefix is returned unchanged (already a bare sha).
func StripObjectKey(key string) string {
	return strings.TrimPrefix(key, objectPrefix)
}
