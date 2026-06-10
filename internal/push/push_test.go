package push

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// repo builds a temp repo with the given files and a managing config.
func repo(t *testing.T, files map[string]string) (string, *config.Config) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{
		Version: 1,
		Storage: config.Storage{Location: "home-pi"},
		Rules:   config.Rules{MinSize: "5MB", Include: []string{"**/*.pdf"}, AutoDelete: true},
	}
	return root, cfg
}

func deps(b backend.Backend) Deps {
	return Deps{
		Backend:     b,
		Preflight:   func(context.Context) error { return nil },
		Whois:       func(context.Context) (string, error) { return "ibte@laptop", nil },
		GitIdentity: func() string { return "git@fallback" },
		Now:         func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
}

func TestPush_NewFile_OnePut(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{"a.pdf": "alpha"})
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1}

	res, err := Run(ctx, root, cfg, lk, deps(b), Options{})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(res.Uploaded) != 1 || b.Puts != 1 {
		t.Fatalf("uploaded=%v Puts=%d, want 1/1", res.Uploaded, b.Puts)
	}
	// blob present + lock written
	if m, _ := b.Stat(ctx, "objects/"+sha([]byte("alpha"))); !m.Exists {
		t.Error("blob not present after push")
	}
	lk2, err := lock.Load(filepath.Join(root, "tailvault.lock"))
	if err != nil {
		t.Fatalf("lock load: %v", err)
	}
	if len(lk2.Entries) != 1 || lk2.Entries[0].Path != "a.pdf" || lk2.Entries[0].Pusher != "ibte@laptop" {
		t.Errorf("lock entry = %+v", lk2.Entries)
	}
}

func TestPush_UnchangedRepush_ZeroPut(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{"a.pdf": "alpha"})
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1}

	if _, err := Run(ctx, root, cfg, lk, deps(b), Options{}); err != nil {
		t.Fatalf("push #1: %v", err)
	}
	first, _ := os.ReadFile(filepath.Join(root, "tailvault.lock"))
	putsAfter1 := b.Puts

	lk2, _ := lock.Load(filepath.Join(root, "tailvault.lock"))
	res, err := Run(ctx, root, cfg, lk2, deps(b), Options{})
	if err != nil {
		t.Fatalf("push #2: %v", err)
	}
	if len(res.Uploaded) != 0 || b.Puts != putsAfter1 {
		t.Errorf("re-push uploaded=%v Puts=%d (was %d), want zero new", res.Uploaded, b.Puts, putsAfter1)
	}
	second, _ := os.ReadFile(filepath.Join(root, "tailvault.lock"))
	if !bytes.Equal(first, second) {
		t.Errorf("lock not byte-identical after unchanged re-push\n--first--\n%s\n--second--\n%s", first, second)
	}
}

func TestPush_ContentPresentElsewhere_ZeroPut(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{"a.pdf": "alpha"})
	b := backend.NewFSBackend(t.TempDir())
	// blob already on node under the content-addressed key.
	_ = b.Put(ctx, "objects/"+sha([]byte("alpha")), bytes.NewReader([]byte("alpha")))
	puts := b.Puts
	lk := &lock.Lock{Version: 1}

	res, err := Run(ctx, root, cfg, lk, deps(b), Options{})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(res.Uploaded) != 0 || len(res.Deduped) != 1 || b.Puts != puts {
		t.Errorf("uploaded=%v deduped=%v Puts=%d, want 0/1/%d", res.Uploaded, res.Deduped, b.Puts, puts)
	}
}

func TestPush_MoveRename_ZeroTransfer(t *testing.T) {
	ctx := context.Background()
	// tree has new.pdf; lock has old.pdf with the same sha; old.pdf gone.
	root, cfg := repo(t, map[string]string{"new.pdf": "shared"})
	b := backend.NewFSBackend(t.TempDir())
	_ = b.Put(ctx, "objects/"+sha([]byte("shared")), bytes.NewReader([]byte("shared")))
	puts := b.Puts
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{
		{Path: "old.pdf", SHA256: sha([]byte("shared")), Size: 6, Location: "home-pi", Pusher: "orig", PushedAt: time.Unix(1, 0).UTC()},
	}}

	res, err := Run(ctx, root, cfg, lk, deps(b), Options{})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if b.Puts != puts || len(res.Renamed) != 1 {
		t.Errorf("rename: Puts=%d (want %d), Renamed=%v", b.Puts, puts, res.Renamed)
	}
	lk2, _ := lock.Load(filepath.Join(root, "tailvault.lock"))
	if len(lk2.Entries) != 1 || lk2.Entries[0].Path != "new.pdf" || lk2.Entries[0].Pusher != "orig" {
		t.Errorf("renamed entry = %+v, want new.pdf carrying orig pusher", lk2.Entries)
	}
}

func TestPush_Deletion_MarksGC_UnlessPreserve(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{}) // empty tree
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{
		{Path: "d.pdf", SHA256: "sha-d", Size: 1, Location: "home-pi"},
		{Path: "keep.pdf", SHA256: "sha-k", Size: 1, Location: "home-pi", Preserve: true},
	}}

	res, err := Run(ctx, root, cfg, lk, deps(b), Options{})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(res.Dropped) != 2 {
		t.Errorf("dropped = %v, want both entries", res.Dropped)
	}
	if len(res.MarkedGC) != 1 || res.MarkedGC[0] != "sha-d" {
		t.Errorf("MarkedGC = %v, want only sha-d (keep.pdf preserved)", res.MarkedGC)
	}
	if b.Deletes != 0 {
		t.Errorf("Deletes = %d, want 0 (mark-only; sweep is task-16)", b.Deletes)
	}
}

func TestPush_NodeDown_AbortsZeroPutUnadvancedLock(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{"a.pdf": "alpha"})
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1}

	d := deps(b)
	d.Preflight = func(context.Context) error { return tserr.NodeOfflineErr("home-pi", errors.New("down")) }

	_, err := Run(ctx, root, cfg, lk, d, Options{})
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.NodeOffline {
		t.Fatalf("push: want TV-NODE-01, got %v", err)
	}
	if b.Puts != 0 {
		t.Errorf("Puts = %d on node-down, want 0", b.Puts)
	}
	if _, err := os.Stat(filepath.Join(root, "tailvault.lock")); !os.IsNotExist(err) {
		t.Errorf("lock should not be written on node-down preflight failure")
	}
}

func TestPush_WhoisFallbackToGit(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{"a.pdf": "alpha"})
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1}
	d := deps(b)
	d.Whois = func(context.Context) (string, error) { return "", errors.New("whois failed") }

	if _, err := Run(ctx, root, cfg, lk, d, Options{}); err != nil {
		t.Fatalf("push: %v", err)
	}
	lk2, _ := lock.Load(filepath.Join(root, "tailvault.lock"))
	if lk2.Entries[0].Pusher != "git@fallback" {
		t.Errorf("pusher = %q, want git@fallback", lk2.Entries[0].Pusher)
	}
}

func TestPush_DryRun_WritesNothing(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{"a.pdf": "alpha"})
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1}

	res, err := Run(ctx, root, cfg, lk, deps(b), Options{DryRun: true})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(res.Uploaded) != 1 {
		t.Errorf("dry-run should still report the plan: uploaded=%v", res.Uploaded)
	}
	if b.Puts != 0 {
		t.Errorf("dry-run Puts = %d, want 0", b.Puts)
	}
	if _, err := os.Stat(filepath.Join(root, "tailvault.lock")); !os.IsNotExist(err) {
		t.Errorf("dry-run must not write the lock")
	}
}

func TestPush_PutVerifyFailure_NotRecorded(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{"a.pdf": "alpha"})
	lk := &lock.Lock{Version: 1}
	d := deps(&forgetfulBackend{})

	_, err := Run(ctx, root, cfg, lk, d, Options{})
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.ObjMissing {
		t.Fatalf("want TV-OBJ-01 on post-Put verify miss, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tailvault.lock")); !os.IsNotExist(err) {
		t.Errorf("lock must not be written when a blob fails post-Put verify")
	}
}

// forgetfulBackend accepts Put but always reports the key absent — simulating a
// node that silently drops the write, which post-Put Stat verify must catch.
type forgetfulBackend struct{}

func (forgetfulBackend) Stat(context.Context, string) (backend.Meta, error) {
	return backend.Meta{Exists: false}, nil
}
func (forgetfulBackend) Get(context.Context, string, io.Writer) error   { return nil }
func (forgetfulBackend) Put(context.Context, string, io.Reader) error   { return nil }
func (forgetfulBackend) Delete(context.Context, string) error           { return nil }
func (forgetfulBackend) List(context.Context, string) ([]string, error) { return nil, nil }
