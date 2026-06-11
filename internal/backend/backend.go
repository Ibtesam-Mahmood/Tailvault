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
}

// HashObject streams the object at key through SHA-256 and returns its lowercase
// hex digest. It is backend-agnostic (works for FSBackend and SSH); the SSH
// backend may later short-circuit with a remote `sha256sum`. Used by verify
// (task-23) to detect corruption / confirm integrity.
func HashObject(ctx context.Context, b Backend, key string) (string, error) {
	h := sha256.New()
	if err := b.Get(ctx, key, h); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
