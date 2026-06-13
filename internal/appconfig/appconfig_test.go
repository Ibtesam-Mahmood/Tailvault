package appconfig

import (
	"path/filepath"
	"testing"
)

// withTempConfig points XDG_CONFIG_HOME at a temp dir so tests never touch a
// developer's real ~/.config/tailvault.
func withTempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestLoadMissingIsEmptyNoError(t *testing.T) {
	withTempConfig(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load on missing file: unexpected error %v", err)
	}
	if c.TailscalePath != "" {
		t.Fatalf("want empty TailscalePath, got %q", c.TailscalePath)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := withTempConfig(t)
	want := "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
	if err := (Config{TailscalePath: want}).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// File lands under <xdg>/tailvault/config.toml.
	if p, _ := Path(); p != filepath.Join(dir, "tailvault", "config.toml") {
		t.Fatalf("unexpected config path %q", p)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.TailscalePath != want {
		t.Fatalf("round-trip: want %q, got %q", want, got.TailscalePath)
	}
	if TailscalePath() != want {
		t.Fatalf("TailscalePath() convenience: want %q, got %q", want, TailscalePath())
	}
}

func TestTailscalePathEmptyWhenUnset(t *testing.T) {
	withTempConfig(t)
	if got := TailscalePath(); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}
