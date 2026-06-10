package revert

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

func shaOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fixture sets up a repo dir with a lock + a backend, returning their handles.
type fixture struct {
	repo string
	lock string
	be   *backend.FSBackend
}

func newFixture(t *testing.T, entries []lock.Entry, blobs map[string][]byte) fixture {
	t.Helper()
	repo := t.TempDir()
	lockPath := filepath.Join(repo, "tailvault.lock")
	lk := &lock.Lock{Entries: entries}
	if err := lock.Write(lockPath, lk, "tailvault test"); err != nil {
		t.Fatalf("seed lock: %v", err)
	}
	be := backend.NewFSBackend(t.TempDir())
	for sha, content := range blobs {
		if err := be.Put(context.Background(), "objects/"+sha, bytes.NewReader(content)); err != nil {
			t.Fatalf("seed blob %s: %v", sha, err)
		}
	}
	return fixture{repo: repo, lock: lockPath, be: be}
}

func TestRun_RestoresOldVersionAndBytes(t *testing.T) {
	ctx := context.Background()
	older := []byte("version A content")
	newer := []byte("version B content")
	a, b := shaOf(older), shaOf(newer)
	fx := newFixture(t,
		[]lock.Entry{{Path: "doc/x.pdf", SHA256: b, History: true, Versions: []string{b, a}}},
		map[string][]byte{a: older, b: newer},
	)

	err := Run(ctx, Options{RepoRoot: fx.repo, Path: "doc/x.pdf", SHA: a, Backend: fx.be})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Working file now holds blob A's bytes.
	got, err := os.ReadFile(filepath.Join(fx.repo, "doc", "x.pdf"))
	if err != nil {
		t.Fatalf("read working file: %v", err)
	}
	if !bytes.Equal(got, older) {
		t.Errorf("working file = %q, want %q", got, older)
	}

	// Lock current sha repointed to A; versions unchanged.
	lk, err := lock.Load(fx.lock)
	if err != nil {
		t.Fatalf("reload lock: %v", err)
	}
	e, _ := lk.Find("doc/x.pdf")
	if e.SHA256 != a {
		t.Errorf("lock sha = %s, want %s", e.SHA256, a)
	}
	if len(e.Versions) != 2 || e.Versions[0] != b || e.Versions[1] != a {
		t.Errorf("versions changed: %v, want [%s %s]", e.Versions, b, a)
	}
}

func TestRun_HistoryOff(t *testing.T) {
	fx := newFixture(t,
		[]lock.Entry{{Path: "f.pdf", SHA256: "B", History: false}},
		nil,
	)
	err := Run(context.Background(), Options{RepoRoot: fx.repo, Path: "f.pdf", SHA: "A", Backend: fx.be})
	if !errors.Is(err, ErrHistoryOff) {
		t.Errorf("want ErrHistoryOff, got %v", err)
	}
	// Working tree + lock untouched (no file created).
	if _, statErr := os.Stat(filepath.Join(fx.repo, "f.pdf")); !os.IsNotExist(statErr) {
		t.Error("history-off revert must not write a working file")
	}
}

func TestRun_UnknownVersion(t *testing.T) {
	b := shaOf([]byte("B"))
	fx := newFixture(t,
		[]lock.Entry{{Path: "f.pdf", SHA256: b, History: true, Versions: []string{b}}},
		map[string][]byte{b: []byte("B")},
	)
	err := Run(context.Background(), Options{RepoRoot: fx.repo, Path: "f.pdf", SHA: "deadbeef", Backend: fx.be})
	if !errors.Is(err, ErrUnknownVersion) {
		t.Errorf("want ErrUnknownVersion, got %v", err)
	}
}

func TestRun_UnknownPath(t *testing.T) {
	fx := newFixture(t, []lock.Entry{{Path: "f.pdf", SHA256: "B", History: true, Versions: []string{"B"}}}, nil)
	err := Run(context.Background(), Options{RepoRoot: fx.repo, Path: "nope.pdf", SHA: "B", Backend: fx.be})
	if !errors.Is(err, ErrUnknownPath) {
		t.Errorf("want ErrUnknownPath, got %v", err)
	}
}

func TestRun_AlreadyCurrentNoOp(t *testing.T) {
	b := shaOf([]byte("B"))
	fx := newFixture(t,
		[]lock.Entry{{Path: "f.pdf", SHA256: b, History: true, Versions: []string{b, "A"}}},
		map[string][]byte{b: []byte("B")},
	)
	before, _ := os.ReadFile(fx.lock)
	if err := Run(context.Background(), Options{RepoRoot: fx.repo, Path: "f.pdf", SHA: b, Backend: fx.be}); err != nil {
		t.Fatalf("no-op revert errored: %v", err)
	}
	after, _ := os.ReadFile(fx.lock)
	if !bytes.Equal(before, after) {
		t.Error("already-current revert should not rewrite the lock")
	}
}

func TestRun_MissingBlob(t *testing.T) {
	a := shaOf([]byte("A"))
	b := shaOf([]byte("B"))
	// Version A is recorded but its blob is absent from the backend.
	fx := newFixture(t,
		[]lock.Entry{{Path: "f.pdf", SHA256: b, History: true, Versions: []string{b, a}}},
		map[string][]byte{b: []byte("B")},
	)
	err := Run(context.Background(), Options{RepoRoot: fx.repo, Path: "f.pdf", SHA: a, Backend: fx.be})
	var te *tserr.Error
	if !errors.As(err, &te) || te.ExitCode() != 5 {
		t.Errorf("want TV-OBJ-01 exit 5, got %v", err)
	}
}
