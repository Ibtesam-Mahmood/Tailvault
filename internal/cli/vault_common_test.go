package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

func TestParseTarget(t *testing.T) {
	full := "30092d830e26" + "00000000000000000000000000000000000000000000000000000000" // 64-ish hex
	for _, tc := range []struct {
		in      string
		wantID  bool
		wantLoc string
		wantRel string
		wantErr bool
	}{
		{in: "home-pi/media/a.pdf", wantLoc: "home-pi", wantRel: "media/a.pdf"},
		{in: "home-pi", wantLoc: "home-pi", wantRel: ""},
		{in: "30092d830e26", wantID: true},    // 12-hex short prefix
		{in: full[:40], wantID: true},         // long prefix
		{in: "30092D830E26", wantID: true},    // upper-case hex → id (lowered)
		{in: "deadbeef", wantLoc: "deadbeef"}, // 8 hex, < shortIDLen, no slash → treated as a location
		{in: "abc/def", wantLoc: "abc", wantRel: "def"},
		{in: "", wantErr: true},
		{in: "/leading", wantErr: true},
	} {
		got, err := parseTarget(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseTarget(%q) = %+v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseTarget(%q) unexpected err %v", tc.in, err)
			continue
		}
		if got.isID != tc.wantID || got.loc != tc.wantLoc || got.rel != tc.wantRel {
			t.Errorf("parseTarget(%q) = %+v; want isID=%v loc=%q rel=%q", tc.in, got, tc.wantID, tc.wantLoc, tc.wantRel)
		}
	}
}

func TestResolveOutcome(t *testing.T) {
	const id = "30092d830e2600000000000000000000000000000000000000000000000000aa"
	for _, tc := range []struct {
		name     string
		res      fed.Result
		wantWarn bool
		wantErr  bool
		wantCode tserr.Code
		wantExit int
	}{
		{name: "at home", res: fed.Result{Outcome: fed.FoundAtHome}},
		{name: "elsewhere", res: fed.Result{Outcome: fed.FoundElsewhere}, wantWarn: true},
		{name: "partial", res: fed.Result{Outcome: fed.PartialView, Reach: fed.Reach{Unreachable: []string{"pi-2"}}}, wantErr: true, wantCode: tserr.FedPartialView, wantExit: 6},
		{name: "missing", res: fed.Result{Outcome: fed.Missing}, wantErr: true, wantCode: tserr.ObjMissing, wantExit: 5},
	} {
		warn, err := resolveOutcome(tc.res, id)
		if warn != tc.wantWarn {
			t.Errorf("%s: warn = %v, want %v", tc.name, warn, tc.wantWarn)
		}
		if tc.wantErr {
			var te *tserr.Error
			if !errors.As(err, &te) || te.Code != tc.wantCode {
				t.Errorf("%s: err = %v, want code %s", tc.name, err, tc.wantCode)
				continue
			}
			if te.ExitCode() != tc.wantExit {
				t.Errorf("%s: exit = %d, want %d", tc.name, te.ExitCode(), tc.wantExit)
			}
		} else if err != nil {
			t.Errorf("%s: unexpected err %v", tc.name, err)
		}
	}
}

func TestBackendForRegistry(t *testing.T) {
	reg := locations.Registry{Locations: map[string]locations.Location{
		"home-pi": {Node: "home-pi.ts.net", BasePath: "/mnt/ssd/tv", Backend: locations.BackendSSH, User: "ibte"},
		"nas":     {Node: "100.1.2.3", BasePath: "/vault", Backend: locations.BackendTaildrive, Share: "vault"},
	}}
	bf := backendForRegistry(reg)

	b, err := bf(catalog.Member{Name: "home-pi"})
	if err != nil {
		t.Fatalf("ssh member: %v", err)
	}
	if _, ok := b.(*backend.SSH); !ok {
		t.Errorf("ssh member → %T, want *backend.SSH", b)
	}

	b, err = bf(catalog.Member{Name: "nas"})
	if err != nil {
		t.Fatalf("taildrive member: %v", err)
	}
	if _, ok := b.(*backend.Taildrive); !ok {
		t.Errorf("taildrive member → %T, want *backend.Taildrive", b)
	}

	// An unregistered member → config error pointing at `location add`.
	_, err = bf(catalog.Member{Name: "ghost"})
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.ConfigBad {
		t.Errorf("unregistered member: err = %v, want TV-CFG-01", err)
	}
}

func TestLoadRoster(t *testing.T) {
	ctx := context.Background()
	joined := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	members := []catalog.Member{
		{Name: "a", Node: "a.ts.net", JoinedAt: joined, Status: catalog.StatusActive},
		{Name: "b", Node: "b.ts.net", JoinedAt: joined, Status: catalog.StatusActive},
	}
	dirA, dirB := t.TempDir(), t.TempDir()
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(filepath.Join(d, "meta"), 0o755); err != nil {
			t.Fatal(err)
		}
		cat := &catalog.Catalog{
			Version: catalog.SchemaVersion, VaultName: "v", Node: "n",
			Federation: catalog.Federation{FedID: "fed-1", Members: members},
		}
		if err := catalog.WriteAtomic(filepath.Join(d, "meta", "catalog.toml"), cat); err != nil {
			t.Fatalf("write catalog: %v", err)
		}
	}
	reg := locations.Registry{Locations: map[string]locations.Location{
		"a": {Node: "a.ts.net", BasePath: dirA, Backend: locations.BackendTaildrive, Share: "a"},
		"b": {Node: "b.ts.net", BasePath: dirB, Backend: locations.BackendTaildrive, Share: "b"},
	}}

	r, err := loadRoster(ctx, reg)
	if err != nil {
		t.Fatalf("loadRoster: %v", err)
	}
	if r.FedID != "fed-1" || len(r.Members) != 2 {
		t.Errorf("roster = %+v; want fed-1 with 2 members", r)
	}
}

func TestLoadRoster_NoFederation(t *testing.T) {
	// A registry whose locations have no catalog → a clean config error.
	reg := locations.Registry{Locations: map[string]locations.Location{
		"a": {Node: "a", BasePath: t.TempDir(), Backend: locations.BackendTaildrive, Share: "a"},
	}}
	if _, err := loadRoster(context.Background(), reg); err == nil {
		t.Error("loadRoster with no federated catalog: want error, got nil")
	}
}
