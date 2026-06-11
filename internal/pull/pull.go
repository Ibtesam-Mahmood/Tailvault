// Package pull implements the smudge direction: it materialises the real,
// integrity-verified bytes a clone needs from the content-addressed store. v1 is
// eager (Q6) — every locked file not already materialised-and-correct is fetched
// in one run. A missing or mismatched blob is a hard failure (TV-OBJ-01, exit 5)
// and never overwrites the working path with bad bytes.
package pull

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/pointer"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// Result summarises a pull run.
type Result struct {
	Fetched  []string // paths materialised this run
	Skipped  []string // already-correct paths
	Warnings []string // per-entry WARN lines (moved/foreign/left-home) — D5/D28
}

// Deps are the injectable collaborators (testable with the stub Backend).
type Deps struct {
	Backend   backend.Backend
	Preflight func(ctx context.Context) error // tserr on unreachable; nil = ok
	// ResolveEntry is the federation seam (D5): for a FEDERATED lock entry (one
	// carrying an ID) it resolves where the blob actually lives, returning the
	// backend to fetch from and an optional warning. A PartialView or genuinely
	// missing blob is returned as an error (TV-FED-01 exit 6 / TV-OBJ-01 exit 5)
	// — hard-fail is reserved for unprovable states. nil ResolveEntry (or a
	// non-federated entry) falls back to fetching from Backend (the v1 path).
	ResolveEntry func(ctx context.Context, e lock.Entry) (be backend.Backend, warn string, err error)
}

// Run fetches and verifies every locked blob the working tree is missing.
func Run(ctx context.Context, root string, lk *lock.Lock, d Deps) (Result, error) {
	var res Result

	// Preflight first — pull reads from the node, so a down node fails cleanly
	// (exit 4) before the working tree is touched.
	if d.Preflight != nil {
		if err := d.Preflight(ctx); err != nil {
			return Result{}, err
		}
	}

	for _, e := range lk.Entries {
		// A tombstone (Deleted) keeps its blob alive for GC but the file was
		// deliberately removed — NEVER materialise it, or a fresh clone / pull
		// would silently resurrect a file the user deleted (the opposite-direction
		// DESIGN §4 correctness bug). Skip without fetching.
		if e.Deleted {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(e.Path))
		if materializedAndCorrect(full, e.SHA256) {
			res.Skipped = append(res.Skipped, e.Path)
			continue
		}

		// Choose the backend to fetch from. A federated entry (carries an ID)
		// resolves through the federation seam so a moved/foreign home is found
		// (WARN) rather than hard-failing; everything else fetches from the
		// configured backend (the v1 path, byte-for-byte unchanged).
		be := d.Backend
		if d.ResolveEntry != nil && e.ID != "" {
			rbe, warn, rerr := d.ResolveEntry(ctx, e)
			if rerr != nil {
				return Result{}, rerr // PartialView (exit 6) / Missing (exit 5)
			}
			if rbe != nil {
				be = rbe
			}
			if warn != "" {
				res.Warnings = append(res.Warnings, warn)
			}
		}

		// The WARN path still verifies the fetched blob's sha256 against the lock
		// entry — "found elsewhere" never relaxes integrity (task-35 gotcha).
		if err := fetchVerified(ctx, be, full, e.SHA256); err != nil {
			return Result{}, err
		}
		res.Fetched = append(res.Fetched, e.Path)
	}
	return res, nil
}

// materializedAndCorrect reports whether the working file already holds real
// bytes that hash to wantSHA (so it can be skipped). A pointer or missing file
// is not correct and must be fetched.
func materializedAndCorrect(path, wantSHA string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	const sniff = 256
	head := make([]byte, sniff)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if pointer.IsPointer(head) {
		return false // unmaterialised pointer
	}

	h := sha256.New()
	h.Write(head)
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == wantSHA
}

// fetchVerified streams objects/<sha> into a temp file in the destination dir
// while hashing, verifies the sha, then atomically renames into place. Missing
// and mismatched blobs both map to TV-OBJ-01 with distinct messages, and the
// working path is never overwritten with unverified bytes.
func fetchVerified(ctx context.Context, be backend.Backend, dest, wantSHA string) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tv-pull-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }

	h := sha256.New()
	mw := io.MultiWriter(tmp, h)
	if err := be.Get(ctx, "objects/"+wantSHA, mw); err != nil {
		cleanup()
		return err // backend already returns TV-OBJ-01 on a missing blob
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != wantSHA {
		os.Remove(tmpName)
		// Corrupt (present but wrong bytes) is TV-OBJ-01 / exit 5 like "missing",
		// but the user-facing cause + fix MUST differ: a mismatch points at
		// `verify`/re-store, not re-push from another clone (task-15 gotcha,
		// SPEC §5). ObjMissingErr hardcodes a "missing" cause, so build the
		// corruption variant explicitly.
		return &tserr.Error{
			Code:  tserr.ObjMissing,
			Cause: fmt.Sprintf("blob %s is corrupt: sha mismatch (got %s)", wantSHA, got),
			Fix:   "run `tailvault verify` or re-store the blob",
			Err:   fmt.Errorf("sha mismatch: want %s, got %s", wantSHA, got),
		}
	}
	return os.Rename(tmpName, dest)
}
