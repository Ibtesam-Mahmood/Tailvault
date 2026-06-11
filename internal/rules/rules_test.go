package rules

import (
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
)

// fixture mirrors the proposal's [rules] block.
func fixture() *config.Config {
	return &config.Config{
		Version: 1,
		Storage: config.Storage{Location: "home-pi"},
		Rules: config.Rules{
			MinSize: "5MB",
			Include: []string{"**/*.pdf", "**/*.stl", "**/*.3mf", "**/*.pptx"},
			Exclude: []string{"**/*.tmp", "drafts/**"},
			History: false,
			Overrides: []config.Override{
				{Match: "masters/**", History: true, Preserve: true},
			},
		},
	}
}

const fiveMB = 5 * 1024 * 1024

func TestEvaluate(t *testing.T) {
	cfg := fixture()
	cases := []struct {
		name              string
		path              string
		size              int64
		managed           bool
		history, preserve bool
	}{
		{"under threshold no include", "notes.txt", 1 << 20, false, false, false},
		{"at threshold boundary", "data.bin", fiveMB, true, false, false},
		{"over threshold", "big.bin", 10 << 20, true, false, false},
		{"include match small", "art/logo.pdf", 1024, true, false, false},
		{"exclude wins over include", "scratch/note.tmp", 50 << 20, false, false, false},
		{"exclude dir", "drafts/board.pdf", 50 << 20, false, false, false},
		{"override history+preserve", "masters/board.pdf", 50 << 20, true, true, true},
		{"managed no override", "pnp/board.pdf", 50 << 20, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := Evaluate(cfg, c.path, c.size)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if d.Managed != c.managed {
				t.Errorf("Managed = %v, want %v", d.Managed, c.managed)
			}
			if d.History != c.history {
				t.Errorf("History = %v, want %v", d.History, c.history)
			}
			if d.Preserve != c.preserve {
				t.Errorf("Preserve = %v, want %v", d.Preserve, c.preserve)
			}
		})
	}
}

func TestFirstMatchWins(t *testing.T) {
	cfg := fixture()
	cfg.Rules.Overrides = []config.Override{
		{Match: "masters/**", History: true, Preserve: true},
		{Match: "masters/board.pdf", History: false, Preserve: false},
	}
	d, err := Evaluate(cfg, "masters/board.pdf", 50<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !d.History || !d.Preserve {
		t.Errorf("first override should win: got %+v", d)
	}
}

func TestOverrideDoesNotPromoteUnmanaged(t *testing.T) {
	cfg := fixture()
	// An override matching a tmp file (excluded) must not flip it to managed.
	cfg.Rules.Overrides = []config.Override{{Match: "**/*.tmp", History: true, Preserve: true}}
	d, err := Evaluate(cfg, "x/note.tmp", 50<<20)
	if err != nil {
		t.Fatal(err)
	}
	if d.Managed {
		t.Error("excluded file must stay unmanaged despite a matching override")
	}
}

func TestMalformedGlobErrors(t *testing.T) {
	cfg := fixture()
	cfg.Rules.Include = []string{"["}
	if _, err := Evaluate(cfg, "a.pdf", 1); err == nil {
		t.Error("malformed glob should return an error")
	}
}
