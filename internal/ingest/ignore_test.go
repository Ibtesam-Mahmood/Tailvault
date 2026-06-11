package ingest

import "testing"

func TestIgnoreMatching(t *testing.T) {
	ig, err := ParseIgnore([]byte(`
# comment
*.tmp
drafts/
build/**
!drafts/keep.md
`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		rel  string
		want bool
	}{
		{"a.tmp", true},           // *.tmp at root
		{"sub/b.tmp", true},       // *.tmp basename at depth
		{"drafts", true},          // dir itself
		{"drafts/x.md", true},     // under dir
		{"drafts/keep.md", false}, // re-included by negation (last match wins)
		{"build/out/bin", true},   // build/**
		{"src/main.go", false},    // not matched
	}
	for _, c := range cases {
		if got := ig.Match(c.rel, nil); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

func TestExplicitTrackBeatsIgnore(t *testing.T) {
	ig, _ := ParseIgnore([]byte("*.tmp\n"))
	if ig.Match("a.tmp", map[string]bool{"a.tmp": true}) {
		t.Error("explicit track must override an ignore (D22)")
	}
	if !ig.Match("a.tmp", nil) {
		t.Error("without explicit track, *.tmp must be ignored")
	}
}

func TestParseIgnoreBadPattern(t *testing.T) {
	_, err := ParseIgnore([]byte("[unclosed\n"))
	if err == nil {
		t.Fatal("expected an error for an invalid glob")
	}
	if _, ok := err.(*BadPatternError); !ok {
		t.Errorf("want *BadPatternError, got %T", err)
	}
}

func TestEmptyIgnoreMatchesNothing(t *testing.T) {
	ig := &Ignore{}
	if ig.Match("anything/at/all.bin", nil) {
		t.Error("empty ignore must match nothing (track everything)")
	}
}
