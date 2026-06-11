package backend

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// FSBackend stores each key as a file under Root (typically a t.TempDir()). It
// has the same semantics as the SSH backend — Put dedups on Stat, Get of a
// missing key returns TV-OBJ-01 — so engine tests can run it as a faithful
// stand-in for a real Tailscale node.
//
// FSBackend never enforces that an object's bytes hash to its key, so tests can
// deliberately plant a mis-keyed (corrupt) blob to exercise verify.
//
// The exported counters record operations so tests can assert, e.g., that a gc
// dry-run performs zero Deletes while a real sweep performs one, or that a
// re-Put of an existing key transfers nothing (Puts unchanged).
type FSBackend struct {
	Root string

	Puts    int // successful writes (excludes deduped no-op Puts)
	Gets    int // successful reads (Get only; HashObject does NOT bump this)
	Hashes  int // successful HashObject calls
	Deletes int // Delete calls that removed a file
}

// NewFSBackend returns an FSBackend rooted at root.
func NewFSBackend(root string) *FSBackend { return &FSBackend{Root: root} }

// pathFor maps a store-relative, slash-separated key onto a host filesystem path
// under Root.
func (b *FSBackend) pathFor(key string) string {
	return filepath.Join(b.Root, filepath.FromSlash(path.Clean("/"+key)))
}

func (b *FSBackend) Stat(_ context.Context, key string) (Meta, error) {
	fi, err := os.Stat(b.pathFor(key))
	if err != nil {
		if os.IsNotExist(err) {
			return Meta{Exists: false}, nil
		}
		return Meta{}, err
	}
	return Meta{Exists: true, Size: fi.Size()}, nil
}

func (b *FSBackend) Get(_ context.Context, key string, w io.Writer) error {
	f, err := os.Open(b.pathFor(key))
	if err != nil {
		if os.IsNotExist(err) {
			return objMissing(key)
		}
		return err
	}
	defer f.Close()
	if _, err := io.Copy(w, f); err != nil {
		return err
	}
	b.Gets++
	return nil
}

// HashObject hashes the file under Root with the same semantics as the real
// backends (missing -> TV-OBJ-01), so it is a faithful double for the multi-node
// harness (task-39) and the Block 4 suite (task-50). It hashes the bytes in
// place WITHOUT a Get — the counting proof that verify streams zero blob bytes.
func (b *FSBackend) HashObject(_ context.Context, key string) (string, error) {
	f, err := os.Open(b.pathFor(key))
	if err != nil {
		if os.IsNotExist(err) {
			return "", objMissing(key)
		}
		return "", err
	}
	defer f.Close()
	sum, err := hashReader(f)
	if err != nil {
		return "", err
	}
	b.Hashes++
	return sum, nil
}

func (b *FSBackend) Put(ctx context.Context, key string, r io.Reader) error {
	// Content-addressed dedup: if it already exists, transfer nothing.
	m, err := b.Stat(ctx, key)
	if err != nil {
		return err
	}
	if m.Exists {
		return nil
	}

	full := b.pathFor(key)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	// Stream to a temp file then rename, so a partial transfer never leaves a
	// corrupt object in place.
	tmp, err := os.CreateTemp(filepath.Dir(full), ".tv-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, full); err != nil {
		os.Remove(tmpName)
		return err
	}
	b.Puts++
	return nil
}

// PutOverwrite atomically replaces a mutable key (temp + fsync + rename); unlike
// Put it does NOT dedup on Stat, so a second write with different bytes wins. It
// does not touch the Puts counter (which tracks content-addressed object
// transfers for dedup assertions, not in-place metadata overwrites).
func (b *FSBackend) PutOverwrite(_ context.Context, key string, r io.Reader) error {
	return atomicReplace(b.pathFor(key), r)
}

func (b *FSBackend) Delete(_ context.Context, key string) error {
	err := os.Remove(b.pathFor(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil // rm -f semantics
		}
		return err
	}
	b.Deletes++
	return nil
}

func (b *FSBackend) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	err := filepath.WalkDir(b.Root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(b.Root, p)
		if rerr != nil {
			return rerr
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // empty root
		}
		return nil, err
	}
	sort.Strings(keys)
	return keys, nil
}

// objMissing builds the TV-OBJ-01 error for a missing key, wrapping ErrNotExist
// so both errors.Is(err, ErrNotExist) and errors.As(err, **tserr.Error) work.
// The bare sha (not the "objects/" store key) is passed so the message matches
// SPEC §5's "Expected blob <sha> missing".
func objMissing(key string) error {
	return tserr.ObjMissingErr(strings.TrimPrefix(key, "objects/"), ErrNotExist)
}
