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

// Taildrive is a Backend that targets a locally-mounted Taildrive share: every
// operation is a plain os.* call against <root>/objects/<sha> etc., using the
// SAME on-node layout as the SSH backend. No network code, no ssh subprocess —
// Tailscale mounts the share and we treat it as local disk. Per the proposal it
// ships after SSH (opt-in) but satisfies the identical Backend contract.
//
// root is base_path joined with the repo subpath — an ALREADY-mounted local
// path; mounting the share is the user/OS's job, out of scope here.
//
// LIMITATION (v1, accepted deviation): the caller must ensure the share is
// mounted. The command-level preflight (cli.preflightNode) hard-fails an ABSENT
// mountpoint as TV-NODE-01, but an existing-but-unmounted mountpoint is NOT
// detected — in that case writes would land on local disk. Robust live-mount
// detection (a sentinel marker file, or a platform mountpoint check) is a
// recommended follow-up; SSH is the hardened MVP backend.
type Taildrive struct {
	Root string
}

// NewTaildrive builds a Taildrive backend rooted at the mounted share path.
func NewTaildrive(root string) *Taildrive { return &Taildrive{Root: root} }

func (b *Taildrive) pathFor(key string) string {
	return filepath.Join(b.Root, filepath.FromSlash(path.Clean("/"+key)))
}

// nodeErr maps an os error on the share to a typed node condition: a permission
// / read-only failure is TV-NODE-02 (reachable but not writable); anything else
// (e.g. an unmounted share / I/O error) is TV-NODE-01 (offline/unreachable).
func (b *Taildrive) nodeErr(err error) error {
	if os.IsPermission(err) {
		return tserr.NodeNotWritableErr(b.Root, err)
	}
	return tserr.NodeOfflineErr(b.Root, err)
}

func (b *Taildrive) Stat(_ context.Context, key string) (Meta, error) {
	fi, err := os.Stat(b.pathFor(key))
	if err != nil {
		if os.IsNotExist(err) {
			return Meta{Exists: false}, nil // miss is data, not an error (dedup)
		}
		return Meta{}, b.nodeErr(err)
	}
	return Meta{Exists: true, Size: fi.Size()}, nil
}

func (b *Taildrive) Get(_ context.Context, key string, w io.Writer) error {
	f, err := os.Open(b.pathFor(key))
	if err != nil {
		if os.IsNotExist(err) {
			return objMissing(key)
		}
		return b.nodeErr(err)
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// HashObject hashes the blob through the local mount: the share IS the locality,
// so there is nothing remote to shell into on a passive Taildrive share. Missing
// key -> TV-OBJ-01; any other os error -> a typed node condition.
func (b *Taildrive) HashObject(_ context.Context, key string) (string, error) {
	f, err := os.Open(b.pathFor(key))
	if err != nil {
		if os.IsNotExist(err) {
			return "", objMissing(key)
		}
		return "", b.nodeErr(err)
	}
	defer f.Close()
	sum, err := hashReader(f)
	if err != nil {
		return "", b.nodeErr(err)
	}
	return sum, nil
}

func (b *Taildrive) Put(ctx context.Context, key string, r io.Reader) error {
	m, err := b.Stat(ctx, key)
	if err != nil {
		return err
	}
	if m.Exists {
		return nil // content-addressed dedup
	}

	full := b.pathFor(key)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return b.nodeErr(err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".tv-tmp-*")
	if err != nil {
		return b.nodeErr(err)
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return b.nodeErr(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return b.nodeErr(err)
	}
	// Rename within the same dir is atomic on one filesystem — no torn blob if
	// the share hiccups mid-write.
	if err := os.Rename(tmpName, full); err != nil {
		os.Remove(tmpName)
		return b.nodeErr(err)
	}
	return nil
}

// PutOverwrite atomically replaces a mutable key on the mounted share
// (temp + fsync + rename); unlike Put it does NOT dedup on Stat.
func (b *Taildrive) PutOverwrite(_ context.Context, key string, r io.Reader) error {
	if err := atomicReplace(b.pathFor(key), r); err != nil {
		return b.nodeErr(err)
	}
	return nil
}

func (b *Taildrive) Delete(_ context.Context, key string) error {
	if err := os.Remove(b.pathFor(key)); err != nil && !os.IsNotExist(err) {
		return b.nodeErr(err)
	}
	return nil
}

func (b *Taildrive) List(_ context.Context, prefix string) ([]string, error) {
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
			return nil, nil // unmounted/empty share lists as empty
		}
		return nil, err
	}
	sort.Strings(keys)
	return keys, nil
}
