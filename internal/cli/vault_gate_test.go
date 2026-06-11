package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/auth"
	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// errBadExit simulates a non-zero remote exit (e.g. the node's TV-AUTH-01).
var errBadExit = errors.New("exit status 2")

// pwFile writes a --password-file and returns its path.
func pwFile(t *testing.T, pw string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(p, []byte(pw), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGateLocation_TaildriveUngated(t *testing.T) {
	// DEV-46.8: a passive taildrive mount can't run node-side verify, so its
	// mutations are NOT password-gated — gateLocation is a nil no-op regardless
	// of whether a password is supplied (relies on tailnet ACL + mount perms).
	ctx := context.Background()
	loc := locations.Location{Backend: locations.BackendTaildrive, BasePath: t.TempDir(), Share: "s"}
	b := backend.NewTaildrive(loc.BasePath)
	if err := gateLocation(ctx, loc, b, "nas", ""); err != nil {
		t.Errorf("taildrive should be ungated (nil), got %v", err)
	}
	if err := gateLocation(ctx, loc, b, "nas", pwFile(t, "irrelevant")); err != nil {
		t.Errorf("taildrive ungated even with a password file, got %v", err)
	}
}

func TestGateLocation_SSH(t *testing.T) {
	ctx := context.Background()
	loc := locations.Location{Backend: locations.BackendSSH, Node: "home-pi", User: "ibte", BasePath: "/mnt/ssd/tv"}

	// Node exits 0 → match → nil. Assert the candidate reached the node verbatim.
	okRunner := &fakeRunner{handle: func(string, []byte) ([]byte, error) { return nil, nil }}
	if err := gateLocation(ctx, loc, &backend.SSH{User: "ibte", Node: "home-pi", BasePath: "/mnt/ssd/tv", R: okRunner}, "home-pi", pwFile(t, "sesame")); err != nil {
		t.Errorf("ssh correct password: %v", err)
	}
	if string(okRunner.lastIn) != "sesame" || !strings.Contains(okRunner.lastCmd, "node verify-passwd") {
		t.Errorf("gate did not run node verify-passwd with verbatim candidate: in=%q cmd=%q", okRunner.lastIn, okRunner.lastCmd)
	}

	// Node returns TV-AUTH-01 on stderr (exit 2) → rejected → TV-AUTH-01.
	rejRunner := &fakeRunner{handle: func(string, []byte) ([]byte, error) {
		return []byte("TV-AUTH-01: password rejected (fix: ...)\n"), errBadExit
	}}
	err := gateLocation(ctx, loc, &backend.SSH{User: "ibte", Node: "home-pi", BasePath: "/mnt/ssd/tv", R: rejRunner}, "home-pi", pwFile(t, "wrong"))
	if !isTVCode(err, tserr.AuthRequired) {
		t.Errorf("ssh wrong password: want TV-AUTH-01, got %v", err)
	}
}

func TestGateLocation_MemoryVerifierSeam(t *testing.T) {
	// The fedtest auth seam: gateLocation drives the REAL gate (auth.Gate → Verify →
	// argon2id) against an in-memory password for a "protected" member, with NO SSH.
	// This is the backbone of the §16 behavioral auth matrix (fix-46 46.C / task-50).
	ctx := context.Background()
	hf, err := auth.NewHashFile([]byte("sesame"))
	if err != nil {
		t.Fatal(err)
	}
	SetTestGateVerifier(func(name string) (auth.Verifier, bool) {
		switch name {
		case "protected":
			return auth.MemoryVerifier{HF: hf, Set: true}, true
		case "nopw":
			return auth.MemoryVerifier{Set: false}, true // models "no password set"
		default:
			return nil, false // unmanaged → normal SSH/no-op logic
		}
	})
	defer SetTestGateVerifier(nil)

	loc := locations.Location{Backend: locations.BackendTaildrive, BasePath: t.TempDir()}
	b := backend.NewTaildrive(loc.BasePath)

	// Correct password → proceeds (nil).
	if err := gateLocation(ctx, loc, b, "protected", pwFile(t, "sesame")); err != nil {
		t.Errorf("correct password: %v", err)
	}
	// Wrong password → TV-AUTH-01 (driven through the real argon2id verify).
	if err := gateLocation(ctx, loc, b, "protected", pwFile(t, "wrong")); !isTVCode(err, tserr.AuthRequired) {
		t.Errorf("wrong password: want TV-AUTH-01, got %v", err)
	}
	// No password set on the member → TV-AUTH-01.
	if err := gateLocation(ctx, loc, b, "nopw", pwFile(t, "anything")); !isTVCode(err, tserr.AuthRequired) {
		t.Errorf("no password set: want TV-AUTH-01, got %v", err)
	}
	// An unmanaged taildrive member → seam declines → ungated (DEV-46.8 preserved).
	if err := gateLocation(ctx, loc, b, "other", pwFile(t, "x")); err != nil {
		t.Errorf("unmanaged taildrive member must stay ungated, got %v", err)
	}
}

func TestLocationBackend(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := t.TempDir()
	if err := taildriveReg(map[string]string{"home-pi": dir}).Save(); err != nil {
		t.Fatal(err)
	}
	b, loc, err := locationBackend("home-pi")
	if err != nil || loc.BasePath != dir {
		t.Fatalf("locationBackend = (%T, %+v, %v)", b, loc, err)
	}
	if _, ok := b.(*backend.Taildrive); !ok {
		t.Errorf("backend = %T, want *Taildrive", b)
	}
	if _, _, err := locationBackend("ghost"); !isTVCode(err, tserr.ConfigBad) {
		t.Errorf("unknown location: want TV-CFG-01, got %v", err)
	}
}
