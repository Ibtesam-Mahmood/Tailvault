// Package backend defines the storage seam tailvault pushes and pulls through:
// "a path that can hold objects/ and refs/". It ships the Backend interface,
// an SSH implementation that streams blobs over `ssh user@node`, and an
// FSBackend loopback used as the reusable test double for every downstream
// engine test. Storage is content-addressed: Put is a no-op when Stat already
// hits, which is the dedup that makes moves/renames free and re-pushes cheap.
package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

// ErrNotExist is the sentinel a backend wraps when a key is absent, so callers
// can branch with errors.Is(err, backend.ErrNotExist). Missing-key failures are
// also surfaced as a typed tserr.Error (TV-OBJ-01) with this set as the wrapped
// cause, so errors.Is and errors.As both work on the same value.
var ErrNotExist = errors.New("backend: key does not exist")

// Meta is the result of Stat: existence + size.
type Meta struct {
	Exists bool
	Size   int64
}

// Backend is "a path that can hold objects/ and refs/." Keys are store-relative
// paths like "objects/<sha256>" or "refs/<path-id>"; implementations join them
// onto their base path.
type Backend interface {
	// Stat reports whether key exists and its size. A missing key is NOT an
	// error here (returns Meta{Exists:false}, nil) so Put can dedup on it.
	Stat(ctx context.Context, key string) (Meta, error)
	// Get streams the object at key into w. A missing key returns a TV-OBJ-01
	// tserr.Error wrapping ErrNotExist.
	Get(ctx context.Context, key string, w io.Writer) error
	// Put stores r at key. Content-addressed: if Stat(key) already hits, Put is
	// a no-op and transfers zero bytes.
	Put(ctx context.Context, key string, r io.Reader) error
	// Delete removes key. Absence is not an error (rm -f semantics).
	Delete(ctx context.Context, key string) error
	// List returns the keys under prefix (store-relative, slash-separated).
	List(ctx context.Context, prefix string) ([]string, error)
	// HashObject returns the lowercase sha256 hex digest of the stored object
	// WITHOUT streaming its bytes back to the caller: the SSH backend runs
	// `sha256sum` on the node and ships back only the 64-hex digest, while the
	// local-disk backends (FSBackend, Taildrive) hash through their mount. A
	// missing key returns a TV-OBJ-01 tserr.Error wrapping ErrNotExist, exactly
	// like Get. Used by verify (task-23) and every Block 4 remote command that
	// needs a cheap integrity answer.
	HashObject(ctx context.Context, key string) (string, error)
}

// hashReader streams r through SHA-256 and returns its lowercase hex digest.
// Shared by the local-disk backends (FSBackend, Taildrive), which already hold
// the bytes locally and need no remote helper.
func hashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
