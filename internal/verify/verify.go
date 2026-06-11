// Package verify audits the stored vault against the content-addressed
// invariant and the committed lock. It performs two read-only passes:
//
//   - Corruption: re-hash every blob under objects/ and confirm the digest
//     equals its content-addressed key. A mismatch means the stored bytes have
//     rotted — judged against the KEY, independent of the lock (that is the
//     whole point of content addressing).
//   - Missing: for every sha the lock references (current entries + history
//     versions[]), confirm a blob exists on the node. A referenced sha with no
//     blob is a dangling pointer (TV-OBJ-01).
//
// verify never mutates the store — no Put, no Delete.
package verify

import (
	"context"
	"strings"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
)

// objectPrefix is the store prefix for content-addressed blobs.
const objectPrefix = "objects/"

// Report is the outcome of an audit.
type Report struct {
	Checked int       // blobs re-hashed
	Corrupt []Finding // stored bytes whose digest != key
	Missing []Finding // lock shas with no blob on the node
}

// OK reports whether the audit found no problems.
func (r Report) OK() bool { return len(r.Corrupt) == 0 && len(r.Missing) == 0 }

// Finding is a single corruption or missing-blob result.
type Finding struct {
	Key   string   // sha key (= the expected hash)
	Got   string   // computed hash (corruption only)
	Paths []string // lock paths referencing this sha (missing only)
}

// Run executes both passes against the backend and the current branch's lock.
// It returns an error only on an I/O failure talking to the backend (e.g. a
// listing failure); corruption and missing blobs are reported in the Report,
// not as errors, so the caller can print a full audit and map a non-empty
// report to exit 5.
func Run(ctx context.Context, b backend.Backend, lk *lock.Lock) (Report, error) {
	var rep Report

	// Pass 1 — corruption: digest vs key for every stored blob.
	keys, err := b.List(ctx, objectPrefix)
	if err != nil {
		return rep, err
	}
	for _, key := range keys {
		want := strings.TrimPrefix(key, objectPrefix)
		got, err := b.HashObject(ctx, key)
		if err != nil {
			return rep, err
		}
		rep.Checked++
		if got != want {
			rep.Corrupt = append(rep.Corrupt, Finding{Key: want, Got: got})
		}
	}

	// Pass 2 — missing: every lock-referenced sha must have a blob.
	refPaths := referencingPaths(lk)
	for _, sha := range lk.ReferencedSHAs() {
		m, err := b.Stat(ctx, objectPrefix+sha)
		if err != nil {
			return rep, err
		}
		if !m.Exists {
			rep.Missing = append(rep.Missing, Finding{Key: sha, Paths: refPaths[sha]})
		}
	}
	return rep, nil
}

// referencingPaths maps each referenced sha to the lock paths that reference it
// (current sha and history versions), for legible missing-blob reporting.
func referencingPaths(lk *lock.Lock) map[string][]string {
	m := make(map[string][]string)
	for _, e := range lk.Entries {
		m[e.SHA256] = append(m[e.SHA256], e.Path)
		for _, v := range e.Versions {
			if v == e.SHA256 {
				continue // current sha already recorded
			}
			m[v] = append(m[v], e.Path)
		}
	}
	return m
}
