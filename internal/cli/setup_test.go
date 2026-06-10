package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
)

// With no real tailscale binary present, discovery fails and the flow falls back
// to manual entry — a deterministic path for testing the wiring end to end.
func TestSetup_InteractiveManualFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	// name, node, base_path, backend, user
	root.SetIn(strings.NewReader("home-pi\n100.64.0.5\n/data\nssh\nibte\n"))
	root.SetArgs([]string{"setup"})
	if err := root.Execute(); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if !strings.Contains(errb.String(), "discovery unavailable") {
		t.Errorf("expected manual-fallback stderr line, got %q", errb.String())
	}
	reg, err := locations.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := reg.Locations["home-pi"]
	if got.Node != "100.64.0.5" || got.BasePath != "/data" || got.Backend != locations.BackendSSH || got.User != "ibte" {
		t.Errorf("registered entry = %+v", got)
	}
	if !strings.Contains(out.String(), `registered location "home-pi"`) {
		t.Errorf("setup output = %q", out.String())
	}
}

func TestLocationAdd_InteractiveWhenNoBasePath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	// --node bypasses pick-list; then base_path, backend, user prompts.
	root.SetIn(strings.NewReader("/vault\nssh\nbob\n"))
	root.SetArgs([]string{"location", "add", "nas", "--node", "nas.ts.net"})
	if err := root.Execute(); err != nil {
		t.Fatalf("location add interactive: %v", err)
	}
	reg, _ := locations.Load()
	got := reg.Locations["nas"]
	if got.Node != "nas.ts.net" || got.BasePath != "/vault" || got.User != "bob" {
		t.Errorf("entry = %+v", got)
	}
}
