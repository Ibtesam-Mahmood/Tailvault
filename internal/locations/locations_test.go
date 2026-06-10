package locations

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

func TestPath_XDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	got, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	want := filepath.Join("/tmp/xdg", "tailvault", "locations.toml")
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestLoad_MissingFileIsEmptyRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // empty dir, no file
	r, err := Load()
	if err != nil {
		t.Fatalf("Load(missing): unexpected error %v", err)
	}
	if len(r.Locations) != 0 {
		t.Errorf("Load(missing) = %d entries, want 0", len(r.Locations))
	}
}

func TestRegistry_RoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	r := Registry{Locations: map[string]Location{}}
	if err := r.Add("home-pi", Location{
		Node: "home-pi.tailnet-name.ts.net", BasePath: "/mnt/ssd/tailvault",
		Backend: BackendSSH, User: "ibte",
	}); err != nil {
		t.Fatalf("Add ssh: %v", err)
	}
	if err := r.Add("office-nas", Location{
		Node: "100.92.14.7", BasePath: "/vault",
		Backend: BackendTaildrive, Share: "vault",
	}); err != nil {
		t.Fatalf("Add taildrive: %v", err)
	}
	if err := r.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Locations) != 2 {
		t.Fatalf("loaded %d entries, want 2", len(got.Locations))
	}
	if got.Locations["home-pi"] != r.Locations["home-pi"] {
		t.Errorf("home-pi round-trip mismatch: %+v vs %+v", got.Locations["home-pi"], r.Locations["home-pi"])
	}
	if got.Locations["office-nas"] != r.Locations["office-nas"] {
		t.Errorf("office-nas round-trip mismatch: %+v vs %+v", got.Locations["office-nas"], r.Locations["office-nas"])
	}
}

func TestAdd_Validation(t *testing.T) {
	cases := []struct {
		name    string
		loc     Location
		wantErr bool
	}{
		{"ssh ok", Location{Node: "n", BasePath: "/p", Backend: BackendSSH, User: "u"}, false},
		{"ssh no user", Location{Node: "n", BasePath: "/p", Backend: BackendSSH}, true},
		{"taildrive ok", Location{Node: "n", BasePath: "/p", Backend: BackendTaildrive, Share: "s"}, false},
		{"taildrive no share", Location{Node: "n", BasePath: "/p", Backend: BackendTaildrive}, true},
		{"no node", Location{BasePath: "/p", Backend: BackendSSH, User: "u"}, true},
		{"no base_path", Location{Node: "n", Backend: BackendSSH, User: "u"}, true},
		{"empty backend", Location{Node: "n", BasePath: "/p"}, true},
		{"bad backend", Location{Node: "n", BasePath: "/p", Backend: "ftp"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Registry{Locations: map[string]Location{}}
			err := r.Add("x", tc.loc)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Add(%+v): want error, got nil", tc.loc)
				}
				var te *tserr.Error
				if !errors.As(err, &te) || te.ExitCode() != 2 {
					t.Errorf("want config error (exit 2), got %v", err)
				}
			} else if err != nil {
				t.Errorf("Add(%+v): unexpected error %v", tc.loc, err)
			}
		})
	}
}

func TestCheck_Reachability(t *testing.T) {
	loc := Location{Node: "home-pi", BasePath: "/p", Backend: BackendSSH, User: "u"}

	up := Check(context.Background(), "home-pi", loc, func(context.Context, string) error { return nil })
	if !up.Reachable || up.Detail != "online" {
		t.Errorf("reachable check = %+v, want reachable/online", up)
	}

	down := Check(context.Background(), "home-pi", loc, func(context.Context, string) error {
		return tserr.NodeOfflineErr("home-pi", nil)
	})
	if down.Reachable {
		t.Errorf("down check Reachable = true, want false (%+v)", down)
	}

	unknown := Check(context.Background(), "home-pi", loc, nil)
	if unknown.Reachable {
		t.Errorf("nil pinger Reachable = true, want false")
	}
}
