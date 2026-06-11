package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// seedRegistry writes a locations.toml under a temp XDG_CONFIG_HOME with the
// given entries and returns nothing (resolveBackend reads it via locations.Load).
func seedRegistry(t *testing.T, locs map[string]locations.Location) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg := locations.Registry{Locations: locs}
	if err := reg.Save(); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
}

func TestResolveBackend_Selection(t *testing.T) {
	seedRegistry(t, map[string]locations.Location{
		"pi":  {Node: "pi", BasePath: "/mnt/ssd/tv", Backend: locations.BackendSSH, User: "ibte"},
		"nas": {Node: "nas", BasePath: "/vault", Backend: locations.BackendTaildrive, Share: "vault"},
	})

	// ssh → *backend.SSH with joined base (base_path + subpath)
	be, _, err := resolveBackend(context.Background(), &config.Config{Storage: config.Storage{Location: "pi", Subpath: "root"}})
	if err != nil {
		t.Fatalf("ssh resolve: %v", err)
	}
	ssh, ok := be.(*backend.SSH)
	if !ok {
		t.Fatalf("ssh: got %T, want *backend.SSH", be)
	}
	if ssh.BasePath != "/mnt/ssd/tv/root" || ssh.User != "ibte" || ssh.Ping == nil {
		t.Errorf("ssh backend = %+v, want joined base + user + non-nil Ping", ssh)
	}

	// taildrive → *backend.Taildrive
	be, _, err = resolveBackend(context.Background(), &config.Config{Storage: config.Storage{Location: "nas"}})
	if err != nil {
		t.Fatalf("taildrive resolve: %v", err)
	}
	if _, ok := be.(*backend.Taildrive); !ok {
		t.Errorf("taildrive: got %T, want *backend.Taildrive", be)
	}
}

func TestResolveBackend_ConfigErrors(t *testing.T) {
	seedRegistry(t, map[string]locations.Location{
		"pi-nouser":   {Node: "pi", BasePath: "/p", Backend: locations.BackendSSH}, // hand-edited: missing user
		"nas-noshare": {Node: "nas", BasePath: "/v", Backend: locations.BackendTaildrive},
		"weird":       {Node: "x", BasePath: "/x", Backend: "ftp"},
	})

	cases := []string{"missing-name", "pi-nouser", "nas-noshare", "weird"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := resolveBackend(context.Background(), &config.Config{Storage: config.Storage{Location: name}})
			if err == nil {
				t.Fatalf("want config error for %q", name)
			}
			var te *tserr.Error
			if !errors.As(err, &te) || te.ExitCode() != 2 {
				t.Errorf("want TV-CFG exit 2 for %q, got %v", name, err)
			}
		})
	}
}

func TestLocationLs_RunE(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg := locations.Registry{Locations: map[string]locations.Location{
		"pi": {Node: "pi", BasePath: "/p", Backend: locations.BackendSSH, User: "ibte"},
	}}
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"location", "ls"})
	// ls must not fail even though the node is unreachable (no tailscale here).
	if err := root.Execute(); err != nil {
		t.Fatalf("location ls returned error (should be informational): %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("pi")) {
		t.Errorf("location ls output missing entry:\n%s", out.String())
	}
}
