// Package status classifies how the working tree relates to the committed
// tailvault.lock: every managed path lands in exactly one of local-only /
// pushed / drifted / orphaned. Classify is pure (no I/O, no backend) so it is
// trivially table-testable; scanning/hashing and the optional blob-presence
// check live in the caller.
package status

import (
	"sort"

	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
)

// State is one of the four file classifications.
type State string

const (
	LocalOnly State = "local-only" // in tree + managed, no lock entry
	Pushed    State = "pushed"     // tree sha == lock sha
	Drifted   State = "drifted"    // tree sha != lock sha (edited since push)
	Orphaned  State = "orphaned"   // lock entry whose path is gone from the tree
)

// Row is one classified path.
type Row struct {
	Path        string
	State       State
	SHA         string // tree sha (local-only/drifted/pushed) or lock sha (orphaned)
	BlobMissing bool   // pushed but the blob is absent on the node (only when checked)
}

// Classify compares managed tree files (path -> content sha) against lock
// entries (path -> Entry). blobPresent is optional: nil skips the presence
// check; non-nil maps sha -> present and flags a pushed row whose blob is
// missing. Rows are returned sorted by Path.
func Classify(treeSHA map[string]string, locked map[string]lock.Entry, blobPresent map[string]bool) []Row {
	var rows []Row

	for path, sha := range treeSHA {
		e, ok := locked[path]
		// A tombstone is not a live entry: a file reappearing at a tombstoned path
		// reads as local-only (it needs a push to resurrect the entry), never as
		// pushed against the dead sha.
		if !ok || e.Deleted {
			rows = append(rows, Row{Path: path, State: LocalOnly, SHA: sha})
			continue
		}
		if sha == e.SHA256 {
			r := Row{Path: path, State: Pushed, SHA: sha}
			if blobPresent != nil && !blobPresent[sha] {
				r.BlobMissing = true
			}
			rows = append(rows, r)
		} else {
			rows = append(rows, Row{Path: path, State: Drifted, SHA: sha})
		}
	}

	for path, e := range locked {
		// A tombstone (Deleted) is a deliberately retained entry for a removed
		// file whose blob is preserved — it is not a live file and not an orphan,
		// so it produces no status row.
		if e.Deleted {
			continue
		}
		if _, ok := treeSHA[path]; !ok {
			rows = append(rows, Row{Path: path, State: Orphaned, SHA: e.SHA256})
		}
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
	return rows
}

// ByPath indexes lock entries by their path for Classify.
func ByPath(l *lock.Lock) map[string]lock.Entry {
	m := make(map[string]lock.Entry, len(l.Entries))
	for _, e := range l.Entries {
		m[e.Path] = e
	}
	return m
}
