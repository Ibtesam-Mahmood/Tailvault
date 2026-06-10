package gc

import (
	"context"
	"fmt"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
)

// BuildKeepSet is the union of shas referenced by any branch's committed lock —
// each entry's current SHA256 plus, for history-on entries, every sha in
// Versions. branchLocks maps a branch name to its parsed lock; the command
// layer fills it via `git show <branch>:tailvault.lock` + lock.Parse, skipping
// branches with no lock (a nil entry contributes nothing). This union is the
// core Risk-Assessment mitigation: a sha still referenced by ANY branch is kept,
// so a delete on one branch never prunes a blob another branch needs.
func BuildKeepSet(branchLocks map[string]*lock.Lock) KeepSet {
	keep := KeepSet{}
	for _, l := range branchLocks {
		if l == nil {
			continue
		}
		for _, sha := range l.ReferencedSHAs() {
			keep.Add(sha)
		}
	}
	return keep
}

// BuildPreserveSet collects the shas of every preserve-marked entry (its current
// sha plus its history versions) across all branches. preserve is belt-and-
// braces: a preserve blob survives a sweep regardless of keep-set membership.
func BuildPreserveSet(branchLocks map[string]*lock.Lock) KeepSet {
	pres := KeepSet{}
	for _, l := range branchLocks {
		if l == nil {
			continue
		}
		for _, e := range l.Entries {
			if !e.Preserve {
				continue
			}
			pres.Add(e.SHA256)
			for _, v := range e.Versions {
				pres.Add(v)
			}
		}
	}
	return pres
}

// Sweep deletes each eligible blob via the backend, unless dryRun is set (in
// which case it deletes nothing and returns 0). It returns the number of blobs
// deleted. Sweep is the ONLY path that calls Backend.Delete — push merely marks
// (SPEC Q4); this keeps deletes off the push critical path.
func Sweep(ctx context.Context, b backend.Backend, p Plan, dryRun bool) (int, error) {
	if dryRun {
		return 0, nil
	}
	deleted := 0
	for _, sha := range p.Eligible {
		if err := b.Delete(ctx, objectPrefix+sha); err != nil {
			return deleted, fmt.Errorf("gc: delete %s%s: %w", objectPrefix, sha, err)
		}
		deleted++
	}
	return deleted, nil
}
