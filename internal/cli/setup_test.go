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

// `setup --remote` registers a remote node. Discovery is forced to fail via the
// seam, so the flow falls back to manual entry deterministically. The fallback
// stderr line varies by whether the tailscale binary is locatable on the host
// (missing → "Entering manual mode."; present-but-down → "...entering manual
// mode."), so the assertion keys on the common "manual mode" phrase.
func TestSetup_RemoteInteractiveManualFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	forceManualDiscovery(t)

	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	// name, node, base_path, backend, user
	root.SetIn(strings.NewReader("home-pi\n100.64.0.5\n/data\nssh\nibte\n"))
	root.SetArgs([]string{"setup", "--remote"})
	if err := root.Execute(); err != nil {
		t.Fatalf("setup --remote: %v", err)
	}

	if !strings.Contains(errb.String(), "manual mode") {
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

// Plain `tailvault setup` (no --remote) creates a LOCAL store: prompts for a
// name then a store path, and persists a local-backend entry with no node.
func TestSetup_LocalDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()

	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	// name, local store path (name default would derive from the repo folder, but
	// the explicit answer overrides it)
	root.SetIn(strings.NewReader("mylocal\n" + store + "\n"))
	root.SetArgs([]string{"setup"})
	if err := root.Execute(); err != nil {
		t.Fatalf("setup local: %v", err)
	}

	reg, err := locations.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := reg.Locations["mylocal"]
	if got.Backend != locations.BackendLocal || got.BasePath != store || got.Node != "" || got.User != "" {
		t.Errorf("local entry = %+v", got)
	}
	if !strings.Contains(out.String(), `registered local location "mylocal"`) {
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
