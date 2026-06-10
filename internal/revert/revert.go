// Package revert rewinds a history-on file to a prior stored content version.
//
// tailvault revert <path> <sha> repoints the lock entry's current sha256 to the
// chosen <sha> (which must already be one of that entry's recorded versions[]),
// re-materializes the working file from objects/<sha>, and writes the canonical
// lock back. It is the ONLY path that moves a history-on entry's current sha
// backward, and it never reorders or truncates versions[] — the full history
// stays intact so you can revert forward again.
//
// The engine returns sentinel errors for the config/precondition failures
// (history-off, unknown sha, unknown path); the command boundary maps those to
// tserr.ConfigErr (exit 2). A missing or corrupt version blob surfaces a
// TV-OBJ-01 integrity error (exit 5) and leaves the working tree and lock
// untouched.
package revert

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/version"
)

// Sentinel errors for the config/precondition failure modes. The command layer
// wraps these with tserr.ConfigErr so they exit 2.
var (
	// ErrUnknownPath: <path> is not a vault-managed entry in the lock.
	ErrUnknownPath = errors.New("revert: no vault-managed file at path")
	// ErrHistoryOff: the entry is history-off, so it has no prior versions.
	ErrHistoryOff = errors.New("revert: file is history-off (no prior versions, by design)")
	// ErrUnknownVersion: <sha> is not among the entry's recorded versions[].
	ErrUnknownVersion = errors.New("revert: sha is not a recorded version of this path")
)

// Options parameterizes a revert.
type Options struct {
	RepoRoot string          // working-tree root; the file is written at RepoRoot/Path
	LockPath string          // tailvault.lock path; defaults to RepoRoot/tailvault.lock
	Path     string          // logical (slash) repo-relative path to revert
	SHA      string          // target version sha (must be in the entry's versions[])
	Backend  backend.Backend // resolved storage backend
}

// Run performs the revert. On success the lock's current sha256 for Path equals
// SHA and the working file holds that blob's bytes; versions[] is unchanged.
// Staging (git add tailvault.lock) is left to the command layer.
func Run(ctx context.Context, opt Options) error {
	lockPath := opt.LockPath
	if lockPath == "" {
		lockPath = filepath.Join(opt.RepoRoot, "tailvault.lock")
	}
	lk, err := lock.Load(lockPath)
	if err != nil {
		return fmt.Errorf("revert: load lock: %w", err)
	}

	e, ok := lk.Find(opt.Path)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownPath, opt.Path)
	}
	if !e.History {
		return fmt.Errorf("%w: %q", ErrHistoryOff, opt.Path)
	}
	if !contains(e.Versions, opt.SHA) {
		return fmt.Errorf("%w: %s for %q", ErrUnknownVersion, short(opt.SHA), opt.Path)
	}
	if e.SHA256 == opt.SHA {
		return nil // already at that version — no-op
	}

	// Fetch the target blob, verifying integrity before touching the working
	// file, so a corrupt/missing version never leaves a half-written file.
	data, err := fetchVerified(ctx, opt.Backend, opt.SHA)
	if err != nil {
		return err
	}
	full := filepath.Join(opt.RepoRoot, filepath.FromSlash(opt.Path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("revert: prepare dir: %w", err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return fmt.Errorf("revert: write working file: %w", err)
	}

	// Repoint current sha; preserve versions[] order/content exactly.
	e.SHA256 = opt.SHA
	lk.Upsert(e)
	if err := lock.Write(lockPath, lk, "tailvault "+version.Version); err != nil {
		return fmt.Errorf("revert: write lock: %w", err)
	}
	return nil
}

// fetchVerified streams objects/<sha> into memory while hashing, and confirms
// the digest equals sha. A missing blob propagates the backend's TV-OBJ-01; a
// hash mismatch is reported as TV-OBJ-01 (integrity bucket, exit 5).
func fetchVerified(ctx context.Context, b backend.Backend, sha string) ([]byte, error) {
	var buf bytes.Buffer
	h := sha256.New()
	if err := b.Get(ctx, "objects/"+sha, io.MultiWriter(&buf, h)); err != nil {
		return nil, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sha {
		return nil, &tserr.Error{
			Code:  tserr.ObjMissing,
			Cause: fmt.Sprintf("version blob %s failed integrity check (got %s)", sha, got),
			Fix:   "re-push from a clone that has the correct content, or run `tailvault verify`",
		}
	}
	return buf.Bytes(), nil
}

func contains(versions []string, sha string) bool {
	for _, v := range versions {
		if v == sha {
			return true
		}
	}
	return false
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
