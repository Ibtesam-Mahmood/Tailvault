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

func TestMerge_SameShaLiveBeatsTombstone(t *testing.T) {
	// Same path, same sha, one side live and one a tombstone. The merged entry
	// must be live (Deleted=false) regardless of argument order, so pull
	// materializes the file that still exists on a branch.
	live := &Lock{Entries: []Entry{{Path: "a", SHA256: "X", Deleted: false}}}
	tomb := &Lock{Entries: []Entry{{Path: "a", SHA256: "X", Deleted: true}}}
	for _, tc := range []struct {
		name         string
		ours, theirs *Lock
	}{
		{"live-ours", live, tomb},
		{"tomb-ours", tomb, live},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := Merge(nil, tc.ours, tc.theirs)
			e, ok := m.Find("a")
			if !ok || e.Deleted {
				t.Errorf("live must beat tombstone: got Deleted=%v (ok=%v)", e.Deleted, ok)
			}
		})
	}
}

func TestMerge_SameShaBothTombstonesStayDeleted(t *testing.T) {
	// Both sides tombstones -> the merged entry remains a tombstone (blob kept
	// alive, file stays deleted).
	a := &Lock{Entries: []Entry{{Path: "a", SHA256: "X", Deleted: true}}}
	b := &Lock{Entries: []Entry{{Path: "a", SHA256: "X", Deleted: true}}}
	m, _ := Merge(nil, a, b)
	e, ok := m.Find("a")
	if !ok || !e.Deleted {
		t.Errorf("both-deleted must stay a tombstone: got Deleted=%v (ok=%v)", e.Deleted, ok)
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

// TestMerge_V2FederatedFieldsRideThrough: the union merge driver treats id +
// genesis as ordinary per-entry fields — a disjoint federated entry survives
// intact, a same-path/same-sha federated entry keeps its identity, and the
// merged lock both self-certifies (Validate) and writes canonically. This is the
// Task 24 driver's v2 regression guard (Task 35).
func TestMerge_V2FederatedFieldsRideThrough(t *testing.T) {
	g1, id1 := federatedGenesis(t, "a.pdf")
	g2, id2 := federatedGenesis(t, "b.pdf")

	ours := &Lock{Entries: []Entry{
		{Path: "a.pdf", ID: id1, Genesis: g1, SHA256: "X", Location: "home-pi", PushedAt: ts("2026-06-10T00:00:00Z")},
	}}
	theirs := &Lock{Entries: []Entry{
		// same path+sha as ours (identity must be preserved on the keep-once path)
		{Path: "a.pdf", ID: id1, Genesis: g1, SHA256: "X", Location: "home-pi", PushedAt: ts("2026-06-10T00:00:00Z")},
		// a disjoint federated entry only theirs has
		{Path: "b.pdf", ID: id2, Genesis: g2, SHA256: "Y", Location: "office-nas", PushedAt: ts("2026-06-10T00:00:00Z")},
	}}

	m, err := Merge(nil, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	a, ok := m.Find("a.pdf")
	if !ok || a.ID != id1 || a.Genesis == nil || *a.Genesis != *g1 {
		t.Errorf("federated identity lost on same-path merge: %+v", a)
	}
	b, ok := m.Find("b.pdf")
	if !ok || b.ID != id2 || b.Genesis == nil || *b.Genesis != *g2 {
		t.Errorf("disjoint federated entry lost identity: %+v", b)
	}
	// The merged lock must be a valid, self-certifying v2 lock.
	m.Version = SchemaVersion
	if err := m.Validate(); err != nil {
		t.Errorf("merged v2 lock should self-certify: %v", err)
	}
	// And it must write canonically (no panic / round-trips through Write+Load).
	got, _ := writeAndReload(t, m)
	if err := got.Validate(); err != nil {
		t.Errorf("written merged lock should validate: %v", err)
	}
}
