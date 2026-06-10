package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
)

func TestLocationAddLs_RoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// add
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"location", "add", "home-pi",
		"--node", "home-pi.tailnet.ts.net", "--base-path", "/mnt/ssd/tailvault",
		"--backend", "ssh", "--user", "ibte"})
	if err := root.Execute(); err != nil {
		t.Fatalf("location add: %v", err)
	}
	if !strings.Contains(out.String(), `added location "home-pi"`) {
		t.Errorf("add output = %q", out.String())
	}

	// it persisted
	reg, err := locations.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reg.Locations["home-pi"].User != "ibte" {
		t.Errorf("persisted entry = %+v", reg.Locations["home-pi"])
	}
}

func TestLocationAdd_InvalidIsConfigError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	// ssh backend with no --user is invalid.
	root.SetArgs([]string{"location", "add", "bad", "--node", "n", "--base-path", "/p", "--backend", "ssh"})
	if err := root.Execute(); err == nil {
		t.Fatal("want config error, got nil")
	}
}

func TestPrintLocations_ReachabilityTable(t *testing.T) {
	reg := locations.Registry{Locations: map[string]Location{
		"home-pi":    {Node: "home-pi", BasePath: "/p", Backend: locations.BackendSSH, User: "u"},
		"office-nas": {Node: "office-nas", BasePath: "/v", Backend: locations.BackendTaildrive, Share: "s"},
	}}
	// stub ping: home-pi up, everything else down.
	ping := func(_ context.Context, node string) error {
		if node == "home-pi" {
			return nil
		}
		return errors.New("down")
	}
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := printLocations(context.Background(), cmd, reg, ping); err != nil {
		t.Fatalf("printLocations: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "home-pi") || !strings.Contains(s, "office-nas") {
		t.Errorf("table missing rows: %q", s)
	}
	// home-pi should be reachable (yes), office-nas not (no).
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "home-pi") && !strings.Contains(line, "yes") {
			t.Errorf("home-pi row not marked yes: %q", line)
		}
		if strings.HasPrefix(line, "office-nas") && !strings.Contains(line, "no") {
			t.Errorf("office-nas row not marked no: %q", line)
		}
	}
}

// Location is re-exported here for terser test literals.
type Location = locations.Location
