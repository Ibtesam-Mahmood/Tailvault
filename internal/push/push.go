// Package push implements tailvault's critical path: a green push guarantees the
// bytes landed on the node, or fails loudly leaving nothing half-done. The
// ordering is the whole point — preflight first, then dedup/upload, then the
// lock is written LAST so the repo never gets ahead of storage.
package push

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/history"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/pointer"
	"github.com/Ibtesam-Mahmood/tailvault/internal/rules"
	"github.com/Ibtesam-Mahmood/tailvault/internal/status"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// Options controls a push run.
type Options struct {
	Branch string // informational lock provenance
	DryRun bool   // preflight+scan+diff+report, but write neither blobs nor lock
}

// Result summarises what a push did (or, for dry-run, would do).
type Result struct {
	Uploaded []string // shas Put this run
	Deduped  []string // shas already present on the node (Stat hit, no Put)
	Renamed  []string // "old->new" lock keys renamed (move/rename, zero transfer)
	Dropped  []string // paths whose entries were removed (deletions)
	MarkedGC []string // shas marked for the sweep (Task 16); never deleted here
}

// Deps are the injectable collaborators, so push.Run is testable with the stub
// Backend and a fake tailscale without a real node.
type Deps struct {
	Backend     backend.Backend
	Preflight   func(ctx context.Context) error           // tserr on unreachable; nil = ok
	Whois       func(ctx context.Context) (string, error) // pusher identity
	GitIdentity func() string                             // fallback pusher (git user.email)
	Now         func() time.Time                          // injectable clock for pushed_at
}

// Run executes the full push sequence in the proposal's order against root.
func Run(ctx context.Context, root string, cfg *config.Config, lk *lock.Lock, d Deps, opts Options) (Result, error) {
	var res Result

	// 1. PREFLIGHT FIRST — no byte moves or lock mutation before this returns nil.
	if d.Preflight != nil {
		if err := d.Preflight(ctx); err != nil {
			return Result{}, err
		}
	}

	// 2. Scan the tree for vault-managed files and hash each (pointer-aware).
	managed, err := status.ManagedFiles(cfg, root)
	if err != nil {
		return Result{}, err
	}
	treeSHA, err := status.ScanTree(root, managed)
	if err != nil {
		return Result{}, err
	}

	// Pusher stamp: whois, falling back to git identity (Q7).
	pusher := resolvePusher(ctx, d)

	oldByPath := status.ByPath(lk)
	newEntries := make(map[string]lock.Entry, len(treeSHA))
	handledOld := map[string]bool{}

	// 3. Diff the tree against the lock (deterministic path order).
	for _, path := range sortedKeys(treeSHA) {
		sha := treeSHA[path]

		// (a) Unchanged: identical sha at the same path → no-op, no Stat/Put.
		// A tombstone (Deleted) is NOT unchanged — a file reappearing at a
		// tombstoned path must fall through to (c) so a fresh live entry
		// (Deleted=false, freshly re-evaluated preserve) is written.
		if old, ok := oldByPath[path]; ok && old.SHA256 == sha && !old.Deleted {
			newEntries[path] = old
			continue
		}

		// (b) Move/rename: a *new* path whose sha matches a lock entry whose old
		// path is now absent from the tree → carry the entry forward, zero transfer.
		if _, existed := oldByPath[path]; !existed {
			if src := renameSource(oldByPath, treeSHA, handledOld, sha); src != "" {
				e := oldByPath[src]
				e.Path = path
				newEntries[path] = e
				handledOld[src] = true
				res.Renamed = append(res.Renamed, src+"->"+path)
				continue
			}
		}

		// (c) New or content-changed: Stat, Put only on miss, verify, then record.
		// Size is the REAL content size — sourced from the pointer when the
		// working file is still a clean pointer, never the pointer text length
		// (SPEC §2; also feeds the rule engine the true size).
		size, szerr := status.ContentSize(root, path)
		if szerr != nil {
			return Result{}, szerr
		}
		dec, derr := rules.Evaluate(cfg, path, size)
		if derr != nil {
			return Result{}, derr
		}
		key := "objects/" + sha
		m, serr := d.Backend.Stat(ctx, key)
		if serr != nil {
			return Result{}, serr
		}
		if m.Exists {
			res.Deduped = append(res.Deduped, sha)
		} else {
			if !opts.DryRun {
				if err := uploadAndVerify(ctx, d.Backend, root, path, key); err != nil {
					return Result{}, err
				}
			}
			res.Uploaded = append(res.Uploaded, sha)
		}

		// Content change at an existing path: history-off marks the superseded sha
		// for GC; history-on keeps it (the append below preserves it in versions[]).
		if old, ok := oldByPath[path]; ok && old.SHA256 != sha {
			if !dec.History && !old.Preserve {
				res.MarkedGC = append(res.MarkedGC, old.SHA256)
			}
		}

		// History-on (task-20): append the new sha to refs/<path-id> + versions[]
		// (newest-first, new sha at head) rather than GC-marking the old sha.
		// Superseded versions live in versions[] and stay in GC's keep-set. Skip
		// on dry-run so the node's refs are not mutated.
		var versions []string
		if dec.History && !opts.DryRun {
			versions, err = history.AppendVersion(ctx, d.Backend, history.PathID(path), sha)
			if err != nil {
				return Result{}, err
			}
		}

		newEntries[path] = lock.Entry{
			Path:     path,
			SHA256:   sha,
			Size:     size,
			Location: cfg.Storage.Location,
			PushedAt: d.now(),
			Pusher:   pusher,
			History:  dec.History,
			Preserve: dec.Preserve,
			Versions: versions,
		}
	}

	// 4. Deletions: lock paths absent from the tree (and not rename sources).
	for _, oldPath := range sortedEntryPaths(oldByPath) {
		if _, inTree := treeSHA[oldPath]; inTree {
			continue
		}
		if handledOld[oldPath] {
			continue
		}
		old := oldByPath[oldPath]

		// An existing tombstone: carry it forward unchanged so its preserved blob
		// stays in gc's keep/preserve set across every subsequent push. It was
		// already reported Dropped on the push that first created it.
		if old.Deleted {
			newEntries[oldPath] = old
			continue
		}

		res.Dropped = append(res.Dropped, oldPath)
		if cfg.Rules.AutoDelete && !old.Preserve {
			// Auto-delete on and not preserved: reclaim the blob (mark for sweep).
			res.MarkedGC = append(res.MarkedGC, old.SHA256)
		} else {
			// The blob must survive (preserve set, or auto_delete opted out) even
			// though the file is gone. Keep a TOMBSTONE so gc's keep-set
			// (ReferencedSHAs) and preserve-set still reference the sha. Dropping
			// the entry here is what previously let a later sweep delete a
			// preserved blob — silent data loss against DESIGN §4. This branch is
			// the exact complement of the mark-for-GC condition above, so a blob is
			// never both GC-marked and orphaned from the keep-set.
			t := old
			t.Deleted = true
			newEntries[oldPath] = t
		}
	}

	// 5/6. Write the lock LAST (canonical), unless dry-run.
	if !opts.DryRun {
		lk.Entries = mapToEntries(newEntries)
		lk.Canonicalize()
		generatedBy := pusher
		if opts.Branch != "" {
			generatedBy = pusher + " (" + opts.Branch + ")"
		}
		if err := lock.Write(filepath.Join(root, "tailvault.lock"), lk, generatedBy); err != nil {
			return Result{}, err
		}
	}

	return res, nil
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}

func resolvePusher(ctx context.Context, d Deps) string {
	if d.Whois != nil {
		if id, err := d.Whois(ctx); err == nil && id != "" && id != "@" {
			return id
		}
	}
	if d.GitIdentity != nil {
		if id := d.GitIdentity(); id != "" {
			return id
		}
	}
	return "unknown"
}

// uploadAndVerify streams the working file's real bytes to the node, then
// confirms via a post-Put Stat that the blob is actually present before the
// caller records it — never record a sha the node does not hold.
func uploadAndVerify(ctx context.Context, be backend.Backend, root, path, key string) error {
	full := filepath.Join(root, filepath.FromSlash(path))
	f, err := os.Open(full)
	if err != nil {
		return err
	}
	defer f.Close()

	// A pointer working file has no local bytes to upload.
	head := make([]byte, 256)
	n, _ := io.ReadFull(f, head)
	if pointer.IsPointer(head[:n]) {
		return tserr.ObjMissingErr(key, fmt.Errorf("working file %s is a pointer with no local content; run `tailvault pull` first", path))
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	if err := be.Put(ctx, key, f); err != nil {
		return err
	}
	m, err := be.Stat(ctx, key)
	if err != nil {
		return err
	}
	if !m.Exists {
		return tserr.ObjMissingErr(key, fmt.Errorf("post-Put verify: blob absent after upload of %s", path))
	}
	return nil
}

// renameSource returns an old lock path whose sha matches and whose path is now
// absent from the tree and not yet handled — the source of a move/rename.
func renameSource(oldByPath map[string]lock.Entry, treeSHA map[string]string, handled map[string]bool, sha string) string {
	var cands []string
	for p, e := range oldByPath {
		if e.SHA256 != sha || handled[p] {
			continue
		}
		// A tombstone is not a live file and cannot be the source of a move — its
		// entry is carried forward as-is by the deletion loop.
		if e.Deleted {
			continue
		}
		if _, stillThere := treeSHA[p]; stillThere {
			continue
		}
		cands = append(cands, p)
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Strings(cands)
	return cands[0]
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func sortedEntryPaths(m map[string]lock.Entry) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func mapToEntries(m map[string]lock.Entry) []lock.Entry {
	es := make([]lock.Entry, 0, len(m))
	for _, e := range m {
		es = append(es, e)
	}
	return es
}
