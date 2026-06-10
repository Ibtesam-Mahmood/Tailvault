package lock

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestMerge_DisjointUnion(t *testing.T) {
	ours := &Lock{Entries: []Entry{{Path: "a", SHA256: "X"}}}
	theirs := &Lock{Entries: []Entry{{Path: "b", SHA256: "Y"}}}
	m, err := Merge(nil, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(m.Entries), m.Entries)
	}
	if _, ok := m.Find("a"); !ok {
		t.Error("missing entry a")
	}
	if _, ok := m.Find("b"); !ok {
		t.Error("missing entry b")
	}
}

func TestMerge_SamePathSameSha(t *testing.T) {
	ours := &Lock{Entries: []Entry{{Path: "a", SHA256: "X"}}}
	theirs := &Lock{Entries: []Entry{{Path: "a", SHA256: "X"}}}
	m, _ := Merge(nil, ours, theirs)
	if len(m.Entries) != 1 || m.Entries[0].SHA256 != "X" {
		t.Errorf("want single a@X, got %+v", m.Entries)
	}
}

func TestMerge_DiffShaNewestPushedAtWins(t *testing.T) {
	ours := &Lock{Entries: []Entry{{Path: "a", SHA256: "X", PushedAt: ts("2026-06-10T10:00:00Z")}}}
	theirs := &Lock{Entries: []Entry{{Path: "a", SHA256: "Y", PushedAt: ts("2026-06-10T12:00:00Z")}}}
	m, _ := Merge(nil, ours, theirs)
	e, _ := m.Find("a")
	if e.SHA256 != "Y" {
		t.Errorf("newer pushed_at should win: got %s, want Y", e.SHA256)
	}
}

func TestMerge_TiebreakDeterministic(t *testing.T) {
	when := ts("2026-06-10T10:00:00Z")
	ours := &Lock{Entries: []Entry{{Path: "a", SHA256: "X", PushedAt: when}}}
	theirs := &Lock{Entries: []Entry{{Path: "a", SHA256: "Y", PushedAt: when}}}
	// Greater sha (Y > X) wins, and the result is stable across argument order.
	m1, _ := Merge(nil, ours, theirs)
	m2, _ := Merge(nil, theirs, ours)
	e1, _ := m1.Find("a")
	e2, _ := m2.Find("a")
	if e1.SHA256 != "Y" || e2.SHA256 != "Y" {
		t.Errorf("tiebreak not deterministic: %s / %s, want Y/Y", e1.SHA256, e2.SHA256)
	}
}

func TestMerge_HistoryUnion(t *testing.T) {
	ours := &Lock{Entries: []Entry{
		{Path: "a", SHA256: "Sx", History: true, Versions: []string{"Sx", "X"}, PushedAt: ts("2026-06-10T10:00:00Z")},
	}}
	theirs := &Lock{Entries: []Entry{
		{Path: "a", SHA256: "Sy", History: true, Versions: []string{"Sy", "Y"}, PushedAt: ts("2026-06-10T12:00:00Z")},
	}}
	m, _ := Merge(nil, ours, theirs)
	e, _ := m.Find("a")
	if e.SHA256 != "Sy" {
		t.Fatalf("winner sha = %s, want Sy", e.SHA256)
	}
	want := []string{"Sy", "Y", "Sx", "X"} // winner-first union, deduped
	if !reflect.DeepEqual(e.Versions, want) {
		t.Errorf("versions = %v, want %v", e.Versions, want)
	}
}

func TestMerge_CanonicalOutput(t *testing.T) {
	// Entries given out of order on each side; merged output must be path-sorted
	// and byte-identical to a fresh canonical write of the same logical set.
	ours := &Lock{Entries: []Entry{
		{Path: "z", SHA256: "Z", Location: "home-pi"},
		{Path: "a", SHA256: "A", Location: "home-pi"},
	}}
	theirs := &Lock{Entries: []Entry{
		{Path: "m", SHA256: "M", Location: "home-pi"},
	}}
	m, _ := Merge(nil, ours, theirs)

	dir := t.TempDir()
	mergedPath := filepath.Join(dir, "merged.lock")
	if err := Write(mergedPath, m, "tailvault test"); err != nil {
		t.Fatal(err)
	}

	// Fresh canonical write of the same set in arbitrary order.
	fresh := &Lock{Entries: []Entry{
		{Path: "m", SHA256: "M", Location: "home-pi"},
		{Path: "z", SHA256: "Z", Location: "home-pi"},
		{Path: "a", SHA256: "A", Location: "home-pi"},
	}}
	freshPath := filepath.Join(dir, "fresh.lock")
	if err := Write(freshPath, fresh, "tailvault test"); err != nil {
		t.Fatal(err)
	}

	mb, _ := os.ReadFile(mergedPath)
	fb, _ := os.ReadFile(freshPath)
	if string(mb) != string(fb) {
		t.Errorf("merged output not byte-identical to canonical write:\n--merged--\n%s\n--fresh--\n%s", mb, fb)
	}
}
