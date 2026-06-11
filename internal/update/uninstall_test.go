package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUninstallTargets(t *testing.T) {
	home := t.TempDir()
	cfgRoot := t.TempDir()
	t.Setenv("TAILVAULT_HOME", filepath.Join(home, ".tailvault"))
	t.Setenv("XDG_CONFIG_HOME", cfgRoot)

	// No state dirs exist yet → only the binary is a target.
	targets, err := UninstallTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Label != "binary" {
		t.Fatalf("with no state dirs, expected only the binary, got %+v", targets)
	}

	// Create both state dirs → they become targets.
	mustMkdir(t, filepath.Join(home, ".tailvault"))
	mustMkdir(t, filepath.Join(cfgRoot, "tailvault"))
	targets, err = UninstallTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 3 {
		t.Fatalf("expected binary + 2 state dirs, got %d: %+v", len(targets), targets)
	}

	// Remove the state dirs and confirm they are gone.
	for _, tg := range targets {
		if tg.Label == "binary" {
			continue // don't delete the test runner binary
		}
		if err := Remove(tg); err != nil {
			t.Errorf("Remove(%s): %v", tg.Path, err)
		}
		if _, err := os.Stat(tg.Path); !os.IsNotExist(err) {
			t.Errorf("%s should be gone", tg.Path)
		}
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
