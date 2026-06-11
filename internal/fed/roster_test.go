package fed

import (
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
)

func tm(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func mem(name, status, joined string) catalog.Member {
	return catalog.Member{Name: name, Node: name + ".ts.net", JoinedAt: tm(joined), Status: status}
}

func names(ms []catalog.Member) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Name
	}
	return out
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFromCatalog(t *testing.T) {
	c := &catalog.Catalog{Federation: catalog.Federation{
		FedID:   "abc",
		Members: []catalog.Member{mem("pi-2", "active", "2026-06-11T09:05:00Z"), mem("pi-1", "active", "2026-06-11T09:00:00Z")},
	}}
	r, err := FromCatalog(c)
	if err != nil {
		t.Fatalf("FromCatalog: %v", err)
	}
	if r.FedID != "abc" {
		t.Errorf("FedID = %q, want abc", r.FedID)
	}
	// sorted by name byte-wise ascending
	if got := names(r.Members); !eqStrs(got, []string{"pi-1", "pi-2"}) {
		t.Errorf("members = %v, want [pi-1 pi-2]", got)
	}

	if _, err := FromCatalog(nil); err == nil {
		t.Error("FromCatalog(nil): want error")
	}
	if _, err := FromCatalog(&catalog.Catalog{}); err == nil {
		t.Error("FromCatalog(empty fed_id): want error")
	}
}

func TestMerge_Union(t *testing.T) {
	a := Roster{FedID: "f1", Members: []catalog.Member{mem("pi-1", "active", "2026-06-11T09:00:00Z")}}
	b := Roster{FedID: "f1", Members: []catalog.Member{mem("pi-2", "active", "2026-06-11T09:05:00Z")}}
	got, err := Merge(a, b)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !eqStrs(names(got.Members), []string{"pi-1", "pi-2"}) {
		t.Errorf("union = %v, want [pi-1 pi-2]", names(got.Members))
	}
}

func TestMerge_ConflictNewestWins(t *testing.T) {
	older := Roster{FedID: "f1", Members: []catalog.Member{mem("pi-1", "active", "2026-06-11T09:00:00Z")}}
	newer := Roster{FedID: "f1", Members: []catalog.Member{mem("pi-1", "left", "2026-06-11T10:00:00Z")}}
	got, err := Merge(older, newer)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(got.Members) != 1 || got.Members[0].Status != "left" {
		t.Errorf("conflict winner = %+v, want status=left", got.Members)
	}
}

func TestMerge_StatusRankTiebreak(t *testing.T) {
	// Same joined_at; a terminal status (evicted) must win over active.
	active := Roster{FedID: "f1", Members: []catalog.Member{mem("pi-1", "active", "2026-06-11T09:00:00Z")}}
	evicted := Roster{FedID: "f1", Members: []catalog.Member{mem("pi-1", "evicted", "2026-06-11T09:00:00Z")}}
	got, err := Merge(active, evicted)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got.Members[0].Status != "evicted" {
		t.Errorf("tiebreak winner status = %q, want evicted", got.Members[0].Status)
	}
}

func TestMerge_FedIDMismatch(t *testing.T) {
	a := Roster{FedID: "f1", Members: []catalog.Member{mem("pi-1", "active", "2026-06-11T09:00:00Z")}}
	b := Roster{FedID: "f2", Members: []catalog.Member{mem("pi-2", "active", "2026-06-11T09:05:00Z")}}
	if _, err := Merge(a, b); err == nil {
		t.Error("Merge with mismatched fed_id: want error")
	}
}

func TestMerge_DeterministicRegardlessOfOrder(t *testing.T) {
	a := Roster{FedID: "f1", Members: []catalog.Member{mem("pi-3", "active", "2026-06-11T09:00:00Z"), mem("pi-1", "active", "2026-06-11T09:00:00Z")}}
	b := Roster{FedID: "f1", Members: []catalog.Member{mem("pi-2", "left", "2026-06-11T09:00:00Z")}}
	g1, err := Merge(a, b)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := Merge(b, a)
	if err != nil {
		t.Fatal(err)
	}
	if !eqStrs(names(g1.Members), names(g2.Members)) {
		t.Errorf("order-dependent: %v vs %v", names(g1.Members), names(g2.Members))
	}
	if !eqStrs(names(g1.Members), []string{"pi-1", "pi-2", "pi-3"}) {
		t.Errorf("members = %v, want sorted [pi-1 pi-2 pi-3]", names(g1.Members))
	}
}

func TestMerge_NoArgs(t *testing.T) {
	if _, err := Merge(); err == nil {
		t.Error("Merge() with no rosters: want error")
	}
}

func TestActive_ExcludesLeftEvicted(t *testing.T) {
	r := Roster{FedID: "f1", Members: []catalog.Member{
		mem("a", "active", "2026-06-11T09:00:00Z"),
		mem("b", "left", "2026-06-11T09:00:00Z"),
		mem("c", "evicted", "2026-06-11T09:00:00Z"),
		mem("d", "active", "2026-06-11T09:00:00Z"),
	}}
	if got := names(r.Active()); !eqStrs(got, []string{"a", "d"}) {
		t.Errorf("Active = %v, want [a d]", got)
	}
	// left/evicted still present in the full roster (history for WARN messages).
	if len(r.Members) != 4 {
		t.Errorf("full roster lost members: %d", len(r.Members))
	}
}

func TestFind(t *testing.T) {
	r := Roster{FedID: "f1", Members: []catalog.Member{mem("a", "active", "2026-06-11T09:00:00Z")}}
	if _, ok := r.Find("a"); !ok {
		t.Error("Find(a): want found")
	}
	if _, ok := r.Find("z"); ok {
		t.Error("Find(z): want not found")
	}
}

func TestUnregisteredMembers(t *testing.T) {
	r := Roster{FedID: "f1", Members: []catalog.Member{
		mem("a", "active", "2026-06-11T09:00:00Z"),
		mem("b", "active", "2026-06-11T09:00:00Z"),
		mem("c", "active", "2026-06-11T09:00:00Z"),
	}}
	got := names(UnregisteredMembers(r, []string{"b"}))
	if !eqStrs(got, []string{"a", "c"}) {
		t.Errorf("unregistered = %v, want [a c]", got)
	}
}
