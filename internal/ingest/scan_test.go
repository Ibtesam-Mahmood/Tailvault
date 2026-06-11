package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

var scanT0 = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// setupScan bootstraps a tree with last_scanned = T0 and sets every file's mtime
// to T0, so an untouched scan sees everything as fresh (no re-hash).
func setupScan(t *testing.T) string {
	t.Helper()
	root := makeTree(t)
	ig, _ := LoadIgnore(root)
	plan, err := BuildPlan(root, ig, nil)
	if err != nil {
		t.Fatal(err)
	}
	catPath := filepath.Join(root, "meta", "catalog.toml")
	cat := newCatalog()
	if err := Bootstrap(context.Background(), BootstrapOpts{
		Root: root, Node: testNode, Actor: "tester", Log: &wal.Log{B: backend.NewFSBackend(root)},
		Cat: cat, CatPath: catPath, Plan: plan, Now: func() time.Time { return scanT0 },
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range plan.Files {
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(c.Rel)), scanT0, scanT0); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func loadCat(t *testing.T, root string) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load(filepath.Join(root, "meta", "catalog.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func scanLog(root string) *wal.Log { return &wal.Log{B: backend.NewFSBackend(root)} }

func touch(t *testing.T, root, rel, content string, mtime time.Time) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func diff(t *testing.T, root string, cat *catalog.Catalog, paranoid bool) []Change {
	t.Helper()
	ig, _ := LoadIgnore(root)
	ch, err := Diff(context.Background(), root, ig, cat, paranoid, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func only(t *testing.T, changes []Change, kind ChangeKind) Change {
	t.Helper()
	var found *Change
	for i := range changes {
		if changes[i].Kind == kind {
			if found != nil {
				t.Fatalf("more than one %s change: %+v", kind, changes)
			}
			found = &changes[i]
		}
	}
	if found == nil {
		t.Fatalf("no %s change in %+v", kind, changes)
	}
	return *found
}

func TestScanAdded(t *testing.T) {
	root := setupScan(t)
	touch(t, root, "new.txt", "newborn", time.Now())
	cat := loadCat(t, root)

	ch := diff(t, root, cat, false)
	add := only(t, ch, Added)
	if add.Path != "new.txt" {
		t.Fatalf("added path = %q", add.Path)
	}

	_, _, err := Apply(context.Background(), scanLog(root), cat, filepath.Join(root, "meta", "catalog.toml"),
		testNode, "tester", ch, func() time.Time { return scanT0 })
	if err != nil {
		t.Fatal(err)
	}
	got := loadCat(t, root)
	f, ok := got.Find("new.txt")
	if !ok || f.SyncMode != catalog.SyncModeManual || len(f.ID) != 64 {
		t.Fatalf("new.txt not ingested correctly: %+v ok=%v", f, ok)
	}
}

func TestScanEdited(t *testing.T) {
	root := setupScan(t)
	before, _ := loadCat(t, root).Find("b.txt")
	// edit b.txt; advance mtime past last_scanned.
	touch(t, root, "b.txt", "bravo-EDITED", scanT0.Add(time.Hour))
	cat := loadCat(t, root)

	ch := diff(t, root, cat, false)
	e := only(t, ch, Edited)
	if e.File.ID != before.ID {
		t.Fatalf("edited id changed: %s != %s", e.File.ID, before.ID)
	}

	_, _, err := Apply(context.Background(), scanLog(root), cat, filepath.Join(root, "meta", "catalog.toml"),
		testNode, "tester", ch, func() time.Time { return scanT0.Add(2 * time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	got, _ := loadCat(t, root).Find("b.txt")
	if got.SHA256 == before.SHA256 {
		t.Error("edited sha not updated")
	}
	if !got.LastScanned.After(before.LastScanned) {
		t.Errorf("last_scanned not advanced: %v vs %v", got.LastScanned, before.LastScanned)
	}
	if got.ID != before.ID {
		t.Error("edited id must be unchanged")
	}
	// A scan WAL entry was recorded.
	recs, _ := scanLog(root).Read(context.Background())
	foundScan := false
	for _, r := range recs {
		if r.Entry.OpType == wal.OpScan {
			foundScan = true
		}
	}
	if !foundScan {
		t.Error("no scan WAL entry recorded for edit")
	}
}

func TestScanSuspectParanoid(t *testing.T) {
	root := setupScan(t)
	before, _ := loadCat(t, root).Find("a.txt")
	// Rewrite with DIFFERENT content of the SAME length, then restore mtime to T0
	// so mtime+size look unchanged → corruption-suspect.
	touch(t, root, "a.txt", "ALPHA", scanT0) // "alpha" and "ALPHA" both length 5
	cat := loadCat(t, root)

	// Non-paranoid: mtime/size unchanged → not even hashed → no change.
	if ch := diff(t, root, cat, false); len(ch) != 0 {
		t.Fatalf("non-paranoid scan should see no change, got %+v", ch)
	}
	// Paranoid: hashes everything → Suspect.
	ch := diff(t, root, cat, true)
	s := only(t, ch, Suspect)
	if s.Path != "a.txt" {
		t.Fatalf("suspect path = %q", s.Path)
	}

	applied, skipped, err := Apply(context.Background(), scanLog(root), cat, filepath.Join(root, "meta", "catalog.toml"),
		testNode, "tester", ch, func() time.Time { return scanT0 })
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 || len(skipped) != 1 {
		t.Fatalf("suspect must be skipped, not applied: applied=%d skipped=%d", len(applied), len(skipped))
	}
	got, _ := loadCat(t, root).Find("a.txt")
	if got.SHA256 != before.SHA256 {
		t.Error("suspect must not mutate the catalog sha")
	}
}

func TestScanMovedPreservesID(t *testing.T) {
	root := setupScan(t)
	before, _ := loadCat(t, root).Find("sub/c.txt")
	// move sub/c.txt -> moved/c.txt (rename preserves content + mtime).
	newP := filepath.Join(root, "moved", "c.txt")
	if err := os.MkdirAll(filepath.Dir(newP), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "sub", "c.txt"), newP); err != nil {
		t.Fatal(err)
	}
	cat := loadCat(t, root)

	ch := diff(t, root, cat, false)
	m := only(t, ch, Moved)
	if m.OldPath != "sub/c.txt" || m.Path != "moved/c.txt" {
		t.Fatalf("move paths wrong: %+v", m)
	}
	// No standalone delete/ingest for the moved content.
	for _, c := range ch {
		if c.Kind == Deleted || c.Kind == Added {
			t.Fatalf("move should not also produce %s: %+v", c.Kind, c)
		}
	}

	_, _, err := Apply(context.Background(), scanLog(root), cat, filepath.Join(root, "meta", "catalog.toml"),
		testNode, "tester", ch, func() time.Time { return scanT0 })
	if err != nil {
		t.Fatal(err)
	}
	got := loadCat(t, root)
	if _, ok := got.Find("sub/c.txt"); ok {
		t.Error("old path must be gone from catalog")
	}
	f, ok := got.Find("moved/c.txt")
	if !ok || f.ID != before.ID {
		t.Fatalf("moved id not preserved: %+v ok=%v want id %s", f, ok, before.ID)
	}
}

func TestScanDeleted(t *testing.T) {
	root := setupScan(t)
	if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	cat := loadCat(t, root)
	ch := diff(t, root, cat, false)
	d := only(t, ch, Deleted)
	if d.Path != "a.txt" {
		t.Fatalf("deleted path = %q", d.Path)
	}
	_, _, err := Apply(context.Background(), scanLog(root), cat, filepath.Join(root, "meta", "catalog.toml"),
		testNode, "tester", ch, func() time.Time { return scanT0 })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loadCat(t, root).Find("a.txt"); ok {
		t.Error("deleted entry must be removed from catalog")
	}
}

func TestScanLazyHashing(t *testing.T) {
	root := setupScan(t)
	cat := loadCat(t, root)

	orig := hashFileFunc
	count := 0
	hashFileFunc = func(r, rel string) (string, error) {
		count++
		return orig(r, rel)
	}
	defer func() { hashFileFunc = orig }()

	ch := diff(t, root, cat, false)
	if len(ch) != 0 {
		t.Fatalf("untouched vault should have no changes, got %+v", ch)
	}
	if count != 0 {
		t.Errorf("lazy hashing violated: %d files hashed on an untouched scan", count)
	}
}

func TestScanRaceOpInFlight(t *testing.T) {
	root := setupScan(t)
	cat := loadCat(t, root)
	bEntry, _ := cat.Find("b.txt")

	// A pending op already locks b.txt's blob.
	log := scanLog(root)
	if _, err := log.AppendIntent(context.Background(), wal.Entry{
		OpID: wal.NewOpID(), OpType: wal.OpMove, BlobRefs: []string{bEntry.ID}, Actor: "other",
		CreatedAt: scanT0, Args: map[string]string{"from": "b.txt", "to": "x.txt"},
	}); err != nil {
		t.Fatal(err)
	}

	// Edit both a.txt and b.txt.
	touch(t, root, "a.txt", "alpha-EDIT", scanT0.Add(time.Hour))
	touch(t, root, "b.txt", "bravo-EDIT", scanT0.Add(time.Hour))
	cat = loadCat(t, root)

	ch := diff(t, root, cat, false)
	applied, skipped, err := Apply(context.Background(), log, cat, filepath.Join(root, "meta", "catalog.toml"),
		testNode, "tester", ch, func() time.Time { return scanT0.Add(2 * time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	// a.txt applies; b.txt is skipped (op in flight on its blob).
	if len(applied) != 1 || applied[0].Path != "a.txt" {
		t.Fatalf("expected only a.txt applied, got %+v", applied)
	}
	if len(skipped) != 1 || skipped[0].Path != "b.txt" {
		t.Fatalf("expected b.txt skipped, got %+v", skipped)
	}
}
