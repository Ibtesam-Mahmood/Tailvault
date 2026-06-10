package pull

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

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func okDeps(b backend.Backend) Deps {
	return Deps{Backend: b, Preflight: func(context.Context) error { return nil }}
}

func writePointer(t *testing.T, path, sha string) {
	t.Helper()
	ptr := "tailvault.v1\nsha256 " + sha + "\nsize 5\nlocation home-pi\n"
	if err := os.WriteFile(path, []byte(ptr), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPull_RestoresRealBytes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	content := []byte("alpha")
	s := sha(content)

	b := backend.NewFSBackend(t.TempDir())
	_ = b.Put(ctx, "objects/"+s, bytes.NewReader(content))

	writePointer(t, filepath.Join(root, "a.pdf"), s)
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{{Path: "a.pdf", SHA256: s, Size: 5, Location: "home-pi"}}}

	res, err := Run(ctx, root, lk, okDeps(b))
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(res.Fetched) != 1 {
		t.Fatalf("Fetched = %v, want a.pdf", res.Fetched)
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.pdf"))
	if !bytes.Equal(got, content) {
		t.Errorf("working file = %q, want %q", got, content)
	}
}

func TestPull_IdempotentSkip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	content := []byte("alpha")
	s := sha(content)
	// real correct bytes already present
	if err := os.WriteFile(filepath.Join(root, "a.pdf"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{{Path: "a.pdf", SHA256: s}}}

	res, err := Run(ctx, root, lk, okDeps(b))
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(res.Skipped) != 1 || len(res.Fetched) != 0 {
		t.Errorf("skipped=%v fetched=%v, want a.pdf skipped", res.Skipped, res.Fetched)
	}
	if b.Gets != 0 {
		t.Errorf("Gets = %d, want 0 (skip already-correct)", b.Gets)
	}
}

func TestPull_MissingBlob_TVOBJ01(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := sha([]byte("alpha"))
	writePointer(t, filepath.Join(root, "a.pdf"), s)
	b := backend.NewFSBackend(t.TempDir()) // empty: blob absent
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{{Path: "a.pdf", SHA256: s}}}

	_, err := Run(ctx, root, lk, okDeps(b))
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.ObjMissing {
		t.Fatalf("want TV-OBJ-01, got %v", err)
	}
	// working file untouched (still a pointer)
	got, _ := os.ReadFile(filepath.Join(root, "a.pdf"))
	if !bytes.Contains(got, []byte("tailvault.v1")) {
		t.Errorf("working file was modified on missing blob: %q", got)
	}
}

func TestPull_CorruptBlob_Mismatch_NotOverwritten(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	wantSHA := sha([]byte("alpha"))
	// node serves the WRONG bytes under the wanted key (corruption).
	b := backend.NewFSBackend(t.TempDir())
	_ = b.Put(ctx, "objects/"+wantSHA, bytes.NewReader([]byte("CORRUPT")))

	writePointer(t, filepath.Join(root, "a.pdf"), wantSHA)
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{{Path: "a.pdf", SHA256: wantSHA}}}

	_, err := Run(ctx, root, lk, okDeps(b))
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.ObjMissing {
		t.Fatalf("want TV-OBJ-01 mismatch, got %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.pdf"))
	if !bytes.Contains(got, []byte("tailvault.v1")) {
		t.Errorf("corrupt fetch overwrote working path: %q", got)
	}
	// no leftover temp files in the dir
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if len(e.Name()) > 3 && e.Name()[:3] == ".tv" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestPull_NodeDown_AbortsBeforeFetch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := sha([]byte("alpha"))
	writePointer(t, filepath.Join(root, "a.pdf"), s)
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{{Path: "a.pdf", SHA256: s}}}

	d := Deps{Backend: b, Preflight: func(context.Context) error { return tserr.NodeOfflineErr("home-pi", errors.New("down")) }}
	_, err := Run(ctx, root, lk, d)
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.NodeOffline {
		t.Fatalf("want TV-NODE-01, got %v", err)
	}
	if b.Gets != 0 {
		t.Errorf("Gets = %d on node-down, want 0", b.Gets)
	}
}

func TestPull_MultipleEager_AndPartialStopsClean(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	good := []byte("good-bytes")
	gs := sha(good)
	bad := sha([]byte("expected")) // node will serve wrong bytes for this key

	b := backend.NewFSBackend(t.TempDir())
	_ = b.Put(ctx, "objects/"+gs, bytes.NewReader(good))
	_ = b.Put(ctx, "objects/"+bad, bytes.NewReader([]byte("WRONG"))) // corrupt

	writePointer(t, filepath.Join(root, "one.pdf"), gs)
	writePointer(t, filepath.Join(root, "two.pdf"), bad)
	// Entries ordered so the good one materialises first.
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{
		{Path: "one.pdf", SHA256: gs},
		{Path: "two.pdf", SHA256: bad},
	}}

	_, err := Run(ctx, root, lk, okDeps(b))
	if err == nil {
		t.Fatal("want error on the corrupt second entry")
	}
	// entry 1 materialised
	got1, _ := os.ReadFile(filepath.Join(root, "one.pdf"))
	if !bytes.Equal(got1, good) {
		t.Errorf("one.pdf = %q, want materialised real bytes", got1)
	}
	// entry 2 not half-written (still a pointer)
	got2, _ := os.ReadFile(filepath.Join(root, "two.pdf"))
	if !bytes.Contains(got2, []byte("tailvault.v1")) {
		t.Errorf("two.pdf should remain a pointer, got %q", got2)
	}
}
