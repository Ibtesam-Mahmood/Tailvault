package pull

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	// The user-facing message must name corruption, NOT "missing", and point at
	// verify/re-store — a corrupt blob has a different fix than a gone one.
	if !strings.Contains(te.Error(), "corrupt") && !strings.Contains(te.Error(), "mismatch") {
		t.Errorf("mismatch message should name corruption, got %q", te.Error())
	}
	if strings.Contains(te.Cause, "missing") {
		t.Errorf("mismatch cause must not say 'missing' (misleads remediation): %q", te.Cause)
	}
	if !strings.Contains(te.Fix, "verify") {
		t.Errorf("mismatch fix should point at verify/re-store, got %q", te.Fix)
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

// TestPull_Tombstone_NoResurrection is the qa-review [4] invariant: a fresh
// clone whose committed lock carries a tombstone (Deleted=true) for a
// preserve-deleted file must NOT recreate that file. The blob may still be on the
// node (kept for GC), but pull must never fetch or materialise it — otherwise the
// preserve-GC fix would trade silent data loss for silent un-deletion.
func TestPull_Tombstone_NoResurrection(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir() // fresh clone: nothing materialised yet
	live := []byte("live-bytes")
	ls := sha(live)
	gone := []byte("deleted-but-preserved")
	gs := sha(gone)

	b := backend.NewFSBackend(t.TempDir())
	_ = b.Put(ctx, "objects/"+ls, bytes.NewReader(live))
	_ = b.Put(ctx, "objects/"+gs, bytes.NewReader(gone)) // blob still preserved on node

	writePointer(t, filepath.Join(root, "live.pdf"), ls)
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{
		{Path: "live.pdf", SHA256: ls, Location: "home-pi"},
		{Path: "gone.pdf", SHA256: gs, Location: "home-pi", Preserve: true, Deleted: true},
	}}

	res, err := Run(ctx, root, lk, okDeps(b))
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	// The live file materialises; the tombstone is neither fetched nor reported.
	if len(res.Fetched) != 1 || res.Fetched[0] != "live.pdf" {
		t.Errorf("Fetched = %v, want only live.pdf", res.Fetched)
	}
	for _, p := range append(res.Fetched, res.Skipped...) {
		if p == "gone.pdf" {
			t.Errorf("tombstone gone.pdf appeared in pull result %v", res)
		}
	}
	// The deleted file must NOT exist on disk after pull.
	if _, statErr := os.Stat(filepath.Join(root, "gone.pdf")); !os.IsNotExist(statErr) {
		t.Errorf("gone.pdf was resurrected (stat err = %v), want it absent", statErr)
	}
	// And its blob was never requested from the node.
	if b.Gets != 1 {
		t.Errorf("Gets = %d, want 1 (only live.pdf; tombstone blob never fetched)", b.Gets)
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

// genID is a 64-hex id stand-in for federated-entry tests (the pull engine never
// re-hashes it; resolution + self-cert live above this layer).
const genID = "9f2b1c4d8a019f2b1c4d8a019f2b1c4d8a019f2b1c4d8a019f2b1c4d8a01abcd"

// TestPull_FederatedFoundElsewhere_WARN: a federated entry whose blob is fetched
// from a resolver-supplied backend succeeds, records the fetch, and surfaces the
// WARN line — integrity is still verified against the lock sha.
func TestPull_FederatedFoundElsewhere_WARN(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	content := []byte("moved-bytes")
	s := sha(content)

	// The configured (home) backend is EMPTY; the elsewhere backend holds it.
	home := backend.NewFSBackend(t.TempDir())
	elsewhere := backend.NewFSBackend(t.TempDir())
	_ = elsewhere.Put(ctx, "objects/"+s, bytes.NewReader(content))

	writePointer(t, filepath.Join(root, "a.pdf"), s)
	lk := &lock.Lock{Version: lock.SchemaVersion, Entries: []lock.Entry{
		{Path: "a.pdf", ID: genID, SHA256: s, Size: 5, Location: "home-pi"},
	}}

	d := okDeps(home)
	d.ResolveEntry = func(_ context.Context, e lock.Entry) (backend.Backend, string, error) {
		return elsewhere, e.Path + " moved to office-nas — run `tailvault heal`", nil
	}
	res, err := Run(ctx, root, lk, d)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(res.Fetched) != 1 || len(res.Warnings) != 1 {
		t.Fatalf("fetched=%v warnings=%v, want 1 each", res.Fetched, res.Warnings)
	}
	if !strings.Contains(res.Warnings[0], "office-nas") {
		t.Errorf("warning should name the new home: %q", res.Warnings[0])
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.pdf"))
	if !bytes.Equal(got, content) {
		t.Errorf("blob not materialised from the elsewhere backend: %q", got)
	}
	if home.Gets != 0 {
		t.Errorf("home backend should not have served the blob, Gets=%d", home.Gets)
	}
}

// TestPull_FederatedPartialView_HardFails: a resolver PartialView error aborts
// pull for that entry (exit 6 class) and never overwrites the working file.
func TestPull_FederatedPartialView_HardFails(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := sha([]byte("x"))
	writePointer(t, filepath.Join(root, "a.pdf"), s)
	lk := &lock.Lock{Version: lock.SchemaVersion, Entries: []lock.Entry{
		{Path: "a.pdf", ID: genID, SHA256: s, Location: "home-pi"},
	}}
	d := okDeps(backend.NewFSBackend(t.TempDir()))
	want := tserr.FedPartialViewErr("9f2b1c4d8a01", []string{"pi-2"}, nil)
	d.ResolveEntry = func(_ context.Context, _ lock.Entry) (backend.Backend, string, error) {
		return nil, "", want
	}
	_, err := Run(ctx, root, lk, d)
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.FedPartialView {
		t.Fatalf("want TV-FED partial view, got %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.pdf"))
	if !bytes.Contains(got, []byte("tailvault.v1")) {
		t.Errorf("working file modified under partial view: %q", got)
	}
}

// TestPull_NonFederatedEntry_IgnoresResolver: an entry with no ID never invokes
// the federation seam (v1 path preserved even when a resolver is wired).
func TestPull_NonFederatedEntry_IgnoresResolver(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	content := []byte("plain")
	s := sha(content)
	b := backend.NewFSBackend(t.TempDir())
	_ = b.Put(ctx, "objects/"+s, bytes.NewReader(content))
	writePointer(t, filepath.Join(root, "a.pdf"), s)
	lk := &lock.Lock{Version: lock.SchemaVersion, Entries: []lock.Entry{
		{Path: "a.pdf", SHA256: s, Location: "home-pi"}, // no ID
	}}
	called := false
	d := okDeps(b)
	d.ResolveEntry = func(_ context.Context, _ lock.Entry) (backend.Backend, string, error) {
		called = true
		return nil, "", errors.New("must not be called")
	}
	res, err := Run(ctx, root, lk, d)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if called {
		t.Error("resolver invoked for a non-federated entry")
	}
	if len(res.Fetched) != 1 {
		t.Errorf("fetched=%v, want a.pdf from configured backend", res.Fetched)
	}
}
