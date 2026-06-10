package lock

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixedTime() time.Time {
	return time.Date(2026, 6, 10, 18, 22, 4, 0, time.UTC)
}

func sampleLock() *Lock {
	return &Lock{
		Version:     1,
		GeneratedBy: "tailvault 0.1.0",
		Entries: []Entry{
			{
				Path:     "pnp/board.pdf",
				SHA256:   "9f2b1c",
				Size:     41231873,
				Location: "home-pi",
				PushedAt: fixedTime(),
				Pusher:   "ibte@laptop",
				History:  false,
				Preserve: false,
			},
			{
				Path:     "masters/board.pdf",
				SHA256:   "9f2b1c",
				Size:     41231873,
				Location: "home-pi",
				PushedAt: fixedTime(),
				Pusher:   "ibte@laptop",
				History:  true,
				Preserve: true,
				Versions: []string{"9f2b1c", "7c10aa", "001122"},
			},
		},
	}
}

func writeAndReload(t *testing.T, l *Lock) (*Lock, []byte) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tailvault.lock")
	if err := Write(p, l, "tailvault test"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return got, b
}

func TestRoundTrip(t *testing.T) {
	in := sampleLock()
	got, _ := writeAndReload(t, in)
	if !reflect.DeepEqual(got.Entries, in.Entries) {
		t.Errorf("round-trip mismatch:\n got %+v\n want %+v", got.Entries, in.Entries)
	}
}

func TestCanonicalSort(t *testing.T) {
	l := &Lock{Entries: []Entry{
		{Path: "z.pdf", PushedAt: fixedTime()},
		{Path: "a.pdf", PushedAt: fixedTime()},
		{Path: "m.pdf", PushedAt: fixedTime()},
	}}
	got, _ := writeAndReload(t, l)
	want := []string{"a.pdf", "m.pdf", "z.pdf"}
	for i, e := range got.Entries {
		if e.Path != want[i] {
			t.Errorf("entry %d path = %q, want %q", i, e.Path, want[i])
		}
	}
}

func TestByteStable(t *testing.T) {
	a := sampleLock()
	// b has the same logical content but reversed entry order.
	b := sampleLock()
	b.Entries[0], b.Entries[1] = b.Entries[1], b.Entries[0]

	pa := filepath.Join(t.TempDir(), "a.lock")
	pb := filepath.Join(t.TempDir(), "b.lock")
	if err := Write(pa, a, "tailvault test"); err != nil {
		t.Fatal(err)
	}
	if err := Write(pb, b, "tailvault test"); err != nil {
		t.Fatal(err)
	}
	ba, _ := os.ReadFile(pa)
	bb, _ := os.ReadFile(pb)
	if string(ba) != string(bb) {
		t.Errorf("writes not byte-stable:\n--- a ---\n%s\n--- b ---\n%s", ba, bb)
	}
}

func TestVersionsNewestFirstPreserved(t *testing.T) {
	in := &Lock{Entries: []Entry{{
		Path: "m/x.pdf", SHA256: "v3", Size: 1, Location: "l", PushedAt: fixedTime(),
		History: true, Versions: []string{"v3", "v2", "v1"},
	}}}
	got, _ := writeAndReload(t, in)
	if !reflect.DeepEqual(got.Entries[0].Versions, []string{"v3", "v2", "v1"}) {
		t.Errorf("versions order changed: %v", got.Entries[0].Versions)
	}
}

func TestHistoryOffOmitsVersions(t *testing.T) {
	in := &Lock{Entries: []Entry{{
		Path: "x.pdf", SHA256: "ab", Size: 1, Location: "l", PushedAt: fixedTime(),
	}}}
	_, raw := writeAndReload(t, in)
	if strings.Contains(string(raw), "versions") {
		t.Errorf("history-off entry should omit versions key:\n%s", raw)
	}
}

func TestPushedAtUTC(t *testing.T) {
	loc := time.FixedZone("CEST", 2*3600)
	in := &Lock{Entries: []Entry{{
		Path: "x.pdf", SHA256: "ab", Size: 1, Location: "l",
		PushedAt: time.Date(2026, 6, 10, 20, 22, 4, 0, loc),
	}}}
	got, raw := writeAndReload(t, in)
	if !strings.Contains(string(raw), "2026-06-10T18:22:04Z") {
		t.Errorf("pushed_at not normalized to UTC Z form:\n%s", raw)
	}
	if !got.Entries[0].PushedAt.Equal(fixedTime()) {
		t.Errorf("instant changed: %v", got.Entries[0].PushedAt)
	}
}

func TestUpsertAndRemove(t *testing.T) {
	l := sampleLock()
	n := len(l.Entries)
	// Upsert existing path replaces in place.
	l.Upsert(Entry{Path: "pnp/board.pdf", SHA256: "newsha", PushedAt: fixedTime()})
	if len(l.Entries) != n {
		t.Errorf("Upsert existing changed count: %d", len(l.Entries))
	}
	// Upsert new path appends.
	l.Upsert(Entry{Path: "new/file.pdf", PushedAt: fixedTime()})
	if len(l.Entries) != n+1 {
		t.Errorf("Upsert new did not append: %d", len(l.Entries))
	}
	// Remove drops one, leaves others.
	l.Remove("pnp/board.pdf")
	for _, e := range l.Entries {
		if e.Path == "pnp/board.pdf" {
			t.Error("Remove did not delete the entry")
		}
	}
	if len(l.Entries) != n {
		t.Errorf("after remove count = %d, want %d", len(l.Entries), n)
	}
}
