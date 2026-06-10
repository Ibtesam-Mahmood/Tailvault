package status

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
)

func locked(entries ...lock.Entry) map[string]lock.Entry {
	m := map[string]lock.Entry{}
	for _, e := range entries {
		m[e.Path] = e
	}
	return m
}

func TestClassify_States(t *testing.T) {
	tree := map[string]string{
		"new.pdf":     "aaaa",
		"a.pdf":       "bbbb", // pushed (matches lock)
		"edited.pdf":  "cccc", // drifted (lock has different sha)
		"renamed.pdf": "dddd", // local-only, same sha as orphaned old.pdf
	}
	lk := locked(
		lock.Entry{Path: "a.pdf", SHA256: "bbbb"},
		lock.Entry{Path: "edited.pdf", SHA256: "9999"},
		lock.Entry{Path: "gone.pdf", SHA256: "eeee"}, // orphaned
		lock.Entry{Path: "old.pdf", SHA256: "dddd"},  // orphaned (move/rename pair)
	)
	rows := Classify(tree, lk, nil)

	want := map[string]State{
		"a.pdf":       Pushed,
		"edited.pdf":  Drifted,
		"new.pdf":     LocalOnly,
		"renamed.pdf": LocalOnly,
		"gone.pdf":    Orphaned,
		"old.pdf":     Orphaned,
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	// sorted by path
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Path > rows[i].Path {
			t.Errorf("rows not sorted: %q before %q", rows[i-1].Path, rows[i].Path)
		}
	}
	for _, r := range rows {
		if want[r.Path] != r.State {
			t.Errorf("%s: state = %s, want %s", r.Path, r.State, want[r.Path])
		}
	}
}

func TestClassify_BlobMissingMarker(t *testing.T) {
	tree := map[string]string{"a.pdf": "bbbb"}
	lk := locked(lock.Entry{Path: "a.pdf", SHA256: "bbbb"})

	// present: blob there -> no marker
	rows := Classify(tree, lk, map[string]bool{"bbbb": true})
	if rows[0].State != Pushed || rows[0].BlobMissing {
		t.Errorf("present: %+v, want pushed, no marker", rows[0])
	}
	// absent: pushed but blob missing -> marker
	rows = Classify(tree, lk, map[string]bool{"bbbb": false})
	if rows[0].State != Pushed || !rows[0].BlobMissing {
		t.Errorf("absent: %+v, want pushed + BlobMissing", rows[0])
	}
}

func TestScanTree_HashesAndRespectsPointer(t *testing.T) {
	root := t.TempDir()
	// A real file.
	real := []byte("the real bytes of a managed blob")
	if err := os.WriteFile(filepath.Join(root, "real.bin"), real, 0o644); err != nil {
		t.Fatal(err)
	}
	// A pointer file standing in for an un-smudged managed file.
	ptr := "tailvault.v1\nsha256 " + "f00dcafe" + "\nsize 123\nlocation home-pi\n"
	if err := os.WriteFile(filepath.Join(root, "ptr.bin"), []byte(ptr), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ScanTree(root, []string{"real.bin", "ptr.bin"})
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	if got["ptr.bin"] != "f00dcafe" {
		t.Errorf("pointer sha = %q, want recorded f00dcafe (not a hash of the pointer text)", got["ptr.bin"])
	}
	if got["real.bin"] == "" || got["real.bin"] == "f00dcafe" {
		t.Errorf("real.bin sha = %q, want a real content hash", got["real.bin"])
	}
}

func TestManagedFiles_RuleEngine(t *testing.T) {
	root := t.TempDir()
	// big.bin >= min_size managed; small.txt below and not included.
	if err := os.WriteFile(filepath.Join(root, "big.bin"), make([]byte, 20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "keep.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Version: 1,
		Rules:   config.Rules{Include: []string{"**/*.pdf"}, MinSize: "1000000B"},
	}
	managed, err := ManagedFiles(cfg, root)
	if err != nil {
		t.Fatalf("ManagedFiles: %v", err)
	}
	// keep.pdf matches include; big.bin is below min_size and not included.
	found := map[string]bool{}
	for _, m := range managed {
		found[m] = true
	}
	if !found["keep.pdf"] {
		t.Errorf("keep.pdf should be managed (include glob); got %v", managed)
	}
}
