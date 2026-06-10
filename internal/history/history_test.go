package history

import (
	"regexp"
	"testing"
)

func TestPathID_Stable(t *testing.T) {
	id := PathID("a/b.pdf")
	if id != PathID("a/b.pdf") {
		t.Error("PathID is not deterministic for the same input")
	}
	// Equivalent spellings normalize to the same id.
	for _, eq := range []string{"./a/b.pdf", "a//b.pdf", "a/./b.pdf"} {
		if PathID(eq) != id {
			t.Errorf("PathID(%q) = %s, want same as PathID(%q) = %s", eq, PathID(eq), "a/b.pdf", id)
		}
	}
	// A different path differs.
	if PathID("a/c.pdf") == id {
		t.Error("PathID collided for distinct paths")
	}
}

func TestPathID_ContentIndependentFormat(t *testing.T) {
	id := PathID("masters/board.stl")
	// hex sha256 => 64 lowercase hex chars, filesystem-safe.
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(id) {
		t.Errorf("PathID(%q) = %q, want 64-char lowercase hex", "masters/board.stl", id)
	}
}

func TestRefKey(t *testing.T) {
	want := refPrefix + PathID("a/b.pdf")
	if got := RefKey("a/b.pdf"); got != want {
		t.Errorf("RefKey = %q, want %q", got, want)
	}
	if got := RefKey("./a/b.pdf"); got != want {
		t.Errorf("RefKey not normalized: %q != %q", got, want)
	}
}
