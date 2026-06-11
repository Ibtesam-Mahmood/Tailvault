package lock

import (
	"errors"
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	data := []byte("version = 2\ngenerated_by = \"tailvault test\"\n\n" +
		"[[entry]]\npath = \"a.pdf\"\nsha256 = \"ab\"\nsize = 1\nlocation = \"home-pi\"\n" +
		"pushed_at = 2026-06-10T18:22:04Z\npusher = \"x\"\nhistory = false\npreserve = false\n")
	l, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if l.Version != 2 || len(l.Entries) != 1 || l.Entries[0].Path != "a.pdf" {
		t.Fatalf("Parse got %+v", l)
	}
	// Parse rejects malformed TOML.
	if _, err := Parse([]byte("not = = toml")); err == nil {
		t.Error("Parse should error on malformed TOML")
	}
}

// TestParseVersionGate asserts D29: a v1 (or any non-2) lock is rejected with
// ErrIncompatibleVersion — no tolerance machinery, recreate don't migrate.
func TestParseVersionGate(t *testing.T) {
	v1 := []byte("version = 1\ngenerated_by = \"x\"\n")
	if _, err := Parse(v1); !errors.Is(err, ErrIncompatibleVersion) {
		t.Fatalf("Parse(v1) err = %v, want ErrIncompatibleVersion", err)
	}
	// A lock with no version key (version 0) is equally rejected.
	if _, err := Parse([]byte("generated_by = \"x\"\n")); !errors.Is(err, ErrIncompatibleVersion) {
		t.Fatalf("Parse(no version) err = %v, want ErrIncompatibleVersion", err)
	}
}

func TestFind(t *testing.T) {
	l := sampleLock()
	e, ok := l.Find("pnp/board.pdf")
	if !ok || e.Path != "pnp/board.pdf" {
		t.Errorf("Find existing = %+v, %v", e, ok)
	}
	if _, ok := l.Find("nope.pdf"); ok {
		t.Error("Find missing should return false")
	}
	if zero, ok := l.Find("nope.pdf"); ok || !reflect.DeepEqual(zero, Entry{}) {
		t.Errorf("Find missing should return zero Entry, got %+v", zero)
	}
}

func TestReferencedSHAs(t *testing.T) {
	l := &Lock{Entries: []Entry{
		{Path: "a", SHA256: "s1"},
		{Path: "b", SHA256: "s2", History: true, Versions: []string{"s2", "s3", "s4"}},
		{Path: "c", SHA256: "s1"}, // duplicate current sha → deduped
		{Path: "d", SHA256: ""},   // empty → skipped
	}}
	got := l.ReferencedSHAs()
	want := []string{"s1", "s2", "s3", "s4"} // first-seen order, deduped, no empty
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReferencedSHAs = %v, want %v", got, want)
	}
}

func TestReferencedSHAsEmptyLock(t *testing.T) {
	if got := (&Lock{}).ReferencedSHAs(); len(got) != 0 {
		t.Errorf("empty lock ReferencedSHAs = %v, want empty", got)
	}
}
