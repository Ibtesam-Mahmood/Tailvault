package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return v
}

// sampleCatalog mirrors internal/catalog/testdata/catalog.toml (the SPEC v2 §9
// canonical sample), built in code so the encode-side fixture is explicit.
func sampleCatalog(t *testing.T) *Catalog {
	t.Helper()
	return &Catalog{
		Version:   2,
		VaultName: "root-pnp",
		Node:      "home-pi.tailnet-name.ts.net",
		Federation: Federation{
			FedID: "5f3c9a1e7b8d2c40a16e9f0b3d4c5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d",
			Members: []Member{
				{Name: "home-pi", Node: "home-pi.tailnet-name.ts.net", JoinedAt: ts(t, "2026-06-11T09:00:00Z"), Status: "active"},
				{Name: "office-nas", Node: "100.92.14.7", JoinedAt: ts(t, "2026-06-11T09:05:00Z"), Status: "active"},
			},
		},
		Files: []File{{
			ID: "30092d830e2641b447745655bbe4171675720a1aa8cf80e0ae3736e6e43111f0",
			Genesis: Genesis{
				ContentSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				OriginalPath:  "pnp/board.pdf",
				IngestOpID:    "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6",
				OriginNode:    "home-pi",
			},
			SHA256:      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			Path:        "pnp/board.pdf",
			SyncMode:    "manual",
			Size:        41231873,
			CreatedAt:   ts(t, "2026-06-11T09:10:00Z"),
			UpdatedAt:   ts(t, "2026-06-11T09:10:00Z"),
			LastScanned: ts(t, "2026-06-11T09:10:00Z"),
		}},
	}
}

// The §9 sample must parse and re-encode byte-identically (canonical fixture).
func TestSampleRoundTripByteIdentical(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "catalog.toml"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(want)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, err := Encode(c)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("re-encode not byte-identical:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// The in-code sample encodes to exactly the testdata fixture too.
func TestEncodeMatchesFixture(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "catalog.toml"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Encode(sampleCatalog(t))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("sampleCatalog encode != fixture:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestVersionGate(t *testing.T) {
	for _, v := range []int{1, 3, 0} {
		src := "version = " + strconv.Itoa(v) + "\nvault_name = 'x'\nnode = 'n'\n"
		_, err := Parse([]byte(src))
		if !errors.Is(err, ErrIncompatibleVersion) {
			t.Errorf("version=%d: got %v, want ErrIncompatibleVersion", v, err)
		}
	}
}

// Entries inserted out of order are emitted sorted by path byte-wise ascending,
// and Encode is deterministic regardless of in-memory order (byte-stable).
func TestCanonicalOrderingByteStable(t *testing.T) {
	mk := func(path string) File {
		return File{
			ID:        "0000000000000000000000000000000000000000000000000000000000000000",
			SHA256:    "ab",
			Path:      path,
			SyncMode:  "git",
			Size:      1,
			CreatedAt: ts(t, "2026-06-11T00:00:00Z"),
		}
	}
	a := &Catalog{Version: 2, Files: []File{mk("z.pdf"), mk("a.pdf"), mk("m.pdf")}}
	b := &Catalog{Version: 2, Files: []File{mk("m.pdf"), mk("z.pdf"), mk("a.pdf")}}

	ba, err := Encode(a)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := Encode(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ba) != string(bb) {
		t.Errorf("encode not byte-stable across input order:\n--- a ---\n%s\n--- b ---\n%s", ba, bb)
	}
	// Sorted order: a, m, z.
	ia := strings.Index(string(ba), "path = 'a.pdf'")
	im := strings.Index(string(ba), "path = 'm.pdf'")
	iz := strings.Index(string(ba), "path = 'z.pdf'")
	if !(ia >= 0 && im > ia && iz > im) {
		t.Errorf("entries not sorted byte-wise ascending: a=%d m=%d z=%d\n%s", ia, im, iz, ba)
	}
}

// Unknown sync_mode values parse and round-trip unchanged (open enum, D15).
func TestOpenSyncModeRoundTrips(t *testing.T) {
	src := "version = 2\nvault_name = 'v'\nnode = 'n'\n\n[[file]]\n" +
		"id = '30092d830e2641b447745655bbe4171675720a1aa8cf80e0ae3736e6e43111f0'\n" +
		"genesis = {content_sha256 = 'aa', original_path = 'p', ingest_op_id = 'o', origin_node = 'h'}\n" +
		"sha256 = 'aa'\npath = 'p/x.bin'\nsync_mode = 's3'\nsize = 1\n" +
		"created_at = 2026-06-11T00:00:00Z\nupdated_at = 2026-06-11T00:00:00Z\nlast_scanned = 2026-06-11T00:00:00Z\n"
	c, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Files[0].SyncMode != "s3" {
		t.Fatalf("sync_mode not preserved on parse: %q", c.Files[0].SyncMode)
	}
	out, err := Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "sync_mode = 's3'") {
		t.Errorf("unknown sync_mode lost on round-trip:\n%s", out)
	}
}

func TestWriteAtomicNoDebrisAndReplace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "meta", "catalog.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := WriteAtomic(p, sampleCatalog(t)); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}
	// Reload and confirm content.
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.VaultName != "root-pnp" || len(got.Files) != 1 {
		t.Errorf("reloaded catalog wrong: %+v", got)
	}

	// Overwrite with a mutated catalog; the rename must replace in place.
	c2 := sampleCatalog(t)
	c2.VaultName = "renamed"
	if err := WriteAtomic(p, c2); err != nil {
		t.Fatalf("WriteAtomic replace: %v", err)
	}
	got2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got2.VaultName != "renamed" {
		t.Errorf("replace failed: %q", got2.VaultName)
	}

	// No temp debris left behind.
	ents, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") || strings.HasPrefix(e.Name(), ".catalog-") {
			t.Errorf("temp debris left behind: %s", e.Name())
		}
	}
}

// A write that cannot create its temp file (missing directory) errors and never
// touches a pre-existing good catalog elsewhere.
func TestWriteAtomicFailureLeavesOriginalIntact(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "catalog.toml")
	if err := WriteAtomic(good, sampleCatalog(t)); err != nil {
		t.Fatalf("seed WriteAtomic: %v", err)
	}
	before, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}

	missing := filepath.Join(dir, "no-such-dir", "catalog.toml")
	if err := WriteAtomic(missing, sampleCatalog(t)); err == nil {
		t.Fatal("WriteAtomic to missing dir: expected error, got nil")
	}

	after, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("pre-existing catalog was modified by a failed write")
	}
}

func TestUpsertRemoveFind(t *testing.T) {
	c := sampleCatalog(t)
	n := len(c.Files)

	// Find by path and id.
	if _, ok := c.Find("pnp/board.pdf"); !ok {
		t.Error("Find by path failed")
	}
	if _, ok := c.FindID("30092d830e2641b447745655bbe4171675720a1aa8cf80e0ae3736e6e43111f0"); !ok {
		t.Error("FindID failed")
	}
	if _, ok := c.Find("nope"); ok {
		t.Error("Find returned ok for missing path")
	}

	// Upsert existing path replaces in place.
	repl := c.Files[0]
	repl.SHA256 = "deadbeef"
	c.Upsert(repl)
	if len(c.Files) != n {
		t.Errorf("Upsert existing changed count: %d", len(c.Files))
	}
	if f, _ := c.Find("pnp/board.pdf"); f.SHA256 != "deadbeef" {
		t.Errorf("Upsert did not replace: %q", f.SHA256)
	}

	// Upsert new path appends.
	c.Upsert(File{Path: "new/a.bin", ID: "x"})
	if len(c.Files) != n+1 {
		t.Errorf("Upsert new did not append: %d", len(c.Files))
	}

	// Remove drops it and reports true; missing reports false.
	if !c.Remove("new/a.bin") {
		t.Error("Remove existing returned false")
	}
	if c.Remove("new/a.bin") {
		t.Error("Remove missing returned true")
	}
	if len(c.Files) != n {
		t.Errorf("after remove count = %d, want %d", len(c.Files), n)
	}
}

func TestValidateRejectsBadID(t *testing.T) {
	c := sampleCatalog(t)
	c.Files[0].ID = "tooshort"
	if err := c.Validate(); err == nil {
		t.Error("Validate accepted a non-64-hex id")
	}
}
