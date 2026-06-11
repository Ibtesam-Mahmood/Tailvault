package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

const testNode = "home-pi"

func fixedClock() func() time.Time {
	t := time.Date(2026, 6, 11, 9, 10, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// makeTree builds a deterministic fixture tree and returns its root. It includes
// a reserved meta/ dir and an ignore file to confirm they are excluded.
func makeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "alpha")
	write("b.txt", "bravo")
	write("sub/c.txt", "charlie")
	// reserved areas — must be excluded from ingestion.
	write("meta/catalog.toml", "junk")
	write(".tailvaultignore", "# present but matches nothing here\n")
	return root
}

func newCatalog() *catalog.Catalog {
	return &catalog.Catalog{Version: catalog.SchemaVersion, VaultName: "v", Node: testNode}
}

func runBootstrap(t *testing.T, root string) []byte {
	t.Helper()
	ig, err := LoadIgnore(root)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(root, ig, nil)
	if err != nil {
		t.Fatal(err)
	}
	log := &wal.Log{B: backend.NewFSBackend(root)}
	cat := newCatalog()
	catPath := filepath.Join(root, "meta", "catalog.toml")
	if err := os.MkdirAll(filepath.Dir(catPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Bootstrap(context.Background(), BootstrapOpts{
		Root: root, Node: testNode, Actor: "tester",
		Log: log, Cat: cat, CatPath: catPath, Plan: plan, Now: fixedClock(),
	}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	b, err := os.ReadFile(catPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuildPlanTracksAllExceptReserved(t *testing.T) {
	root := makeTree(t)
	ig, _ := LoadIgnore(root)
	plan, err := BuildPlan(root, ig, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range plan.Files {
		got[c.Rel] = true
	}
	want := []string{"a.txt", "b.txt", "sub/c.txt"}
	if len(got) != len(want) {
		t.Fatalf("plan files = %v, want %v", got, want)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing candidate %q", w)
		}
	}
	for _, bad := range []string{"meta/catalog.toml", ".tailvaultignore"} {
		if got[bad] {
			t.Errorf("reserved/ignored path %q must not be a candidate", bad)
		}
	}
}

func TestBootstrapGenesisAndCatalog(t *testing.T) {
	root := makeTree(t)
	_ = runBootstrap(t, root)
	cat, err := catalog.Load(filepath.Join(root, "meta", "catalog.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Files) != 3 {
		t.Fatalf("want 3 catalog files, got %d", len(cat.Files))
	}
	for _, f := range cat.Files {
		if f.SyncMode != catalog.SyncModeManual {
			t.Errorf("%s sync_mode = %q, want manual", f.Path, f.SyncMode)
		}
		wantID, err := identity.MintID(identity.Genesis{
			ContentSHA256: f.Genesis.ContentSHA256, OriginalPath: f.Genesis.OriginalPath,
			IngestOpID: f.Genesis.IngestOpID, OriginNode: f.Genesis.OriginNode,
		})
		if err != nil {
			t.Fatal(err)
		}
		if f.ID != wantID {
			t.Errorf("%s id = %s, want self-certifying %s", f.Path, f.ID, wantID)
		}
		if f.Genesis.OriginNode != testNode {
			t.Errorf("%s origin_node = %q", f.Path, f.Genesis.OriginNode)
		}
		if f.CreatedAt.IsZero() || f.LastScanned.IsZero() {
			t.Errorf("%s has zero timestamps", f.Path)
		}
	}
}

func TestBootstrapIdempotentRerun(t *testing.T) {
	root := makeTree(t)
	first := runBootstrap(t, root)

	// Second run on the same root with a fresh catalog object.
	log := &wal.Log{B: backend.NewFSBackend(root)}
	before, _ := log.Read(context.Background())
	ig, _ := LoadIgnore(root)
	plan, _ := BuildPlan(root, ig, nil)
	cat := newCatalog()
	catPath := filepath.Join(root, "meta", "catalog.toml")
	if err := Bootstrap(context.Background(), BootstrapOpts{
		Root: root, Node: testNode, Actor: "tester",
		Log: log, Cat: cat, CatPath: catPath, Plan: plan, Now: fixedClock(),
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := log.Read(context.Background())
	if len(after) != len(before) {
		t.Errorf("re-run added WAL entries: before %d, after %d", len(before), len(after))
	}
	second, _ := os.ReadFile(catPath)
	if string(first) != string(second) {
		t.Errorf("re-run changed the catalog:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// appendPendingIngest mirrors Bootstrap's new-file path up to (but not past) the
// WAL intent — simulating a crash after the intent, before catalog/done.
func appendPendingIngest(t *testing.T, root, rel string) {
	t.Helper()
	sum, err := hashFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	opID := ingestOpID(rel)
	g := identity.Genesis{ContentSHA256: sum, OriginalPath: rel, IngestOpID: opID, OriginNode: testNode}
	id, _ := identity.MintID(g)
	log := &wal.Log{B: backend.NewFSBackend(root)}
	_, err = log.AppendIntent(context.Background(), wal.Entry{
		OpID: opID, OpType: wal.OpIngest, BlobRefs: []string{id}, Actor: "tester",
		CreatedAt: fixedClock()(),
		Args: map[string]string{
			"path": rel, "content_sha256": sum, "origin_node": testNode,
			"sync_mode": catalog.SyncModeManual, "size": strconv.FormatInt(fi.Size(), 10),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func walIngestCount(t *testing.T, root string) int {
	t.Helper()
	log := &wal.Log{B: backend.NewFSBackend(root)}
	recs, err := log.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range recs {
		if r.Entry.OpType == wal.OpIngest {
			n++
		}
	}
	return n
}

// Resume from a crash AFTER the WAL intent, BEFORE the catalog/done, converges to
// a byte-identical catalog with no duplicate WAL entries.
func TestResumeAfterIntent(t *testing.T) {
	gold := runBootstrap(t, makeTree(t))

	root := makeTree(t)
	appendPendingIngest(t, root, "b.txt") // crash state: b.txt intent only
	got := runBootstrap(t, root)

	if string(got) != string(gold) {
		t.Errorf("resume-after-intent catalog != gold:\n--- got ---\n%s\n--- gold ---\n%s", got, gold)
	}
	if n := walIngestCount(t, root); n != 3 {
		t.Errorf("duplicate WAL ingest entries after resume: got %d, want 3", n)
	}
}

// Resume from a crash AFTER the catalog upsert, BEFORE the done marker.
func TestResumeAfterCatalog(t *testing.T) {
	gold := runBootstrap(t, makeTree(t))

	root := makeTree(t)
	appendPendingIngest(t, root, "b.txt")
	// Simulate the catalog flush having landed (row present) but the done marker
	// lost: write a catalog containing b.txt's row, leave the intent pending.
	log := &wal.Log{B: backend.NewFSBackend(root)}
	recs, _ := log.Read(context.Background())
	cat := newCatalog()
	for _, r := range recs {
		row, err := rowFromEntry(r.Entry, testNode)
		if err != nil {
			t.Fatal(err)
		}
		cat.Upsert(row)
	}
	catPath := filepath.Join(root, "meta", "catalog.toml")
	if err := catalog.WriteAtomic(catPath, cat); err != nil {
		t.Fatal(err)
	}

	got := runBootstrap(t, root)
	if string(got) != string(gold) {
		t.Errorf("resume-after-catalog catalog != gold:\n--- got ---\n%s\n--- gold ---\n%s", got, gold)
	}
	if n := walIngestCount(t, root); n != 3 {
		t.Errorf("duplicate WAL ingest entries after resume: got %d, want 3", n)
	}
}

func TestSymlinkSkipped(t *testing.T) {
	root := makeTree(t)
	if err := os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	ig, _ := LoadIgnore(root)
	plan, err := BuildPlan(root, ig, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range plan.Files {
		if c.Rel == "link.txt" {
			t.Error("symlink must be skipped, not ingested")
		}
	}
}
