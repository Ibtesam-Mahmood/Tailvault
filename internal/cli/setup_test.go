package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
)

// forceManualDiscovery overrides the discovery seam so the interactive flow runs
// deterministically with no real tailscale daemon (peer discovery returns an
// error → manual fallback), regardless of whether the host has Tailscale.
func forceManualDiscovery(t *testing.T) {
	t.Helper()
	prev := statusForDiscovery
	statusForDiscovery = func(context.Context) (tailscale.Status, error) {
		return tailscale.Status{}, errors.New("test: discovery disabled")
	}
	t.Cleanup(func() { statusForDiscovery = prev })
}

// Discovery is forced to fail via the seam, so the flow falls back to manual
// entry deterministically — no dependence on the host's tailscale binary.
func TestSetup_InteractiveManualFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	forceManualDiscovery(t)

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
	forceManualDiscovery(t) // --node bypasses the pick-list, but keep it deterministic

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
