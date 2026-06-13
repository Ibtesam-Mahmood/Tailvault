package tailscale

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// makeFakeBin writes an executable file and returns its path.
func makeFakeBin(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLocatePrefersEnvOverride(t *testing.T) {
	// Isolate from the dev machine's PATH/config so only the override can win.
	t.Setenv("PATH", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	want := makeFakeBin(t, dir, "ts-override")
	t.Setenv("TAILVAULT_TAILSCALE", want)

	got, ok := Locate()
	if !ok {
		t.Fatal("Locate: want found via env override, got not found")
	}
	if got != want {
		t.Fatalf("Locate: want %q, got %q", want, got)
	}
}

func TestLocateUsesConfigWhenNoEnvOrPath(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("TAILVAULT_TAILSCALE", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	want := makeFakeBin(t, dir, "ts-config")

	// Persist via the same config the resolver reads.
	cfgDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "tailvault")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"),
		[]byte("tailscale_path = "+strconvQuote(want)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, ok := Locate()
	if !ok || got != want {
		t.Fatalf("Locate via config: want %q found, got %q ok=%v", want, got, ok)
	}
}

func TestLocateFindsOnPath(t *testing.T) {
	t.Setenv("TAILVAULT_TAILSCALE", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	makeFakeBin(t, dir, binName())
	t.Setenv("PATH", dir)

	got, ok := Locate()
	if !ok {
		t.Fatal("Locate: want found on PATH, got not found")
	}
	if filepath.Dir(got) != dir {
		t.Fatalf("Locate: want a binary in %q, got %q", dir, got)
	}
}

func TestLocateNotFoundAnywhere(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("TAILVAULT_TAILSCALE", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// well-known paths almost certainly don't exist in the test sandbox; if a dev
	// machine happens to have Tailscale installed there, skip rather than fail.
	if _, ok := Locate(); ok {
		if anyWellKnownExists() {
			t.Skip("a real tailscale binary exists in a well-known location on this machine")
		}
		t.Fatal("Locate: want not found, got found")
	}
}

func TestResolveBinaryFallsBackToBareName(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("TAILVAULT_TAILSCALE", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if anyWellKnownExists() {
		t.Skip("real tailscale present in a well-known location")
	}
	if got := resolveBinary(); got != binName() {
		t.Fatalf("resolveBinary fallback: want %q, got %q", binName(), got)
	}
}

func TestBinNamePerOS(t *testing.T) {
	got := binName()
	if runtime.GOOS == "windows" && got != "tailscale.exe" {
		t.Fatalf("windows binName: want tailscale.exe, got %q", got)
	}
	if runtime.GOOS != "windows" && got != "tailscale" {
		t.Fatalf("unix binName: want tailscale, got %q", got)
	}
}

func anyWellKnownExists() bool {
	for _, p := range wellKnownPaths() {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// strconvQuote quotes s as a TOML basic string without pulling in strconv at the
// call site (keeps the test's intent obvious).
func strconvQuote(s string) string { return `"` + s + `"` }
