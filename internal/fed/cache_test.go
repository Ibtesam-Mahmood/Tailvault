package fed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func summary(name string, ids ...string) MemberSummary {
	return MemberSummary{
		Name:      name,
		Node:      name + ".ts.net",
		Status:    "active",
		Reachable: true,
		LastSeen:  tm("2026-06-11T10:00:00Z"),
		FileCount: len(ids),
		IDs:       ids,
	}
}

func snap(fedID string, members ...MemberSummary) Snapshot {
	return Snapshot{FedID: fedID, TakenAt: tm("2026-06-11T10:00:00Z"), Members: members}
}

func TestCache_LoadEmpty(t *testing.T) {
	c := &Cache{Dir: t.TempDir()}
	cur, prev, err := c.Load()
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if cur != nil || prev != nil {
		t.Errorf("empty cache: want (nil,nil), got (%v,%v)", cur, prev)
	}
}

func TestCache_RecordRotation(t *testing.T) {
	dir := t.TempDir()
	c := &Cache{Dir: dir}

	first := snap("f1", summary("pi-1", "id-a"))
	if err := c.Record(first); err != nil {
		t.Fatalf("Record first: %v", err)
	}
	// After one Record: current exists, previous does not.
	if _, _, err := c.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "previous.toml")); !os.IsNotExist(err) {
		t.Error("previous.toml should not exist after one Record")
	}

	second := snap("f1", summary("pi-1", "id-a"), summary("pi-2", "id-b"))
	if err := c.Record(second); err != nil {
		t.Fatalf("Record second: %v", err)
	}
	cur, prev, err := c.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil || len(cur.Members) != 2 {
		t.Errorf("current should be the second snapshot: %+v", cur)
	}
	if prev == nil || len(prev.Members) != 1 {
		t.Errorf("previous should be the first snapshot: %+v", prev)
	}
}

func TestCache_RecordRoundTrip(t *testing.T) {
	c := &Cache{Dir: t.TempDir()}
	in := snap("fed-xyz", summary("pi-1", "id-a", "id-b"))
	if err := c.Record(in); err != nil {
		t.Fatal(err)
	}
	cur, _, err := c.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cur.FedID != "fed-xyz" || len(cur.Members) != 1 {
		t.Fatalf("round-trip mismatch: %+v", cur)
	}
	m := cur.Members[0]
	if m.Name != "pi-1" || m.FileCount != 2 || len(m.IDs) != 2 {
		t.Errorf("member round-trip mismatch: %+v", m)
	}
	if !m.LastSeen.Equal(tm("2026-06-11T10:00:00Z")) {
		t.Errorf("last_seen round-trip mismatch: %v", m.LastSeen)
	}
}

func TestCache_NoTmpDebris(t *testing.T) {
	dir := t.TempDir()
	c := &Cache{Dir: dir}
	if err := c.Record(snap("f1", summary("pi-1", "id-a"))); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tv-cache-") {
			t.Errorf("temp debris left behind: %s", e.Name())
		}
	}
}

func TestCache_WasKnown(t *testing.T) {
	c := &Cache{Dir: t.TempDir()}
	// First snapshot has id-old; second (current) has id-new only. id-old must
	// still be "known" via the previous snapshot.
	if err := c.Record(snap("f1", summary("pi-1", "id-old"))); err != nil {
		t.Fatal(err)
	}
	if err := c.Record(snap("f1", summary("pi-2", "id-new"))); err != nil {
		t.Fatal(err)
	}

	if who, known := c.WasKnown("id-new"); !known || who != "pi-2" {
		t.Errorf("WasKnown(id-new) = (%q,%v), want (pi-2,true)", who, known)
	}
	if who, known := c.WasKnown("id-old"); !known || who != "pi-1" {
		t.Errorf("WasKnown(id-old) = (%q,%v), want (pi-1,true) via previous", who, known)
	}
	if _, known := c.WasKnown("id-never"); known {
		t.Error("WasKnown(id-never): want false")
	}
}

func TestCache_WasKnownEmpty(t *testing.T) {
	c := &Cache{Dir: t.TempDir()}
	if _, known := c.WasKnown("anything"); known {
		t.Error("WasKnown on empty cache: want false")
	}
}

func TestSnapshot_RosterReconstruct(t *testing.T) {
	s := snap("f1", summary("pi-2", "id-b"), summary("pi-1", "id-a"))
	r := s.Roster()
	if r.FedID != "f1" {
		t.Errorf("FedID = %q", r.FedID)
	}
	if !eqStrs(names(r.Members), []string{"pi-1", "pi-2"}) {
		t.Errorf("reconstructed roster = %v, want sorted [pi-1 pi-2]", names(r.Members))
	}
}
