package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/auth"
	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// writeNodePasswd writes a node hash file under base (base/meta/auth/passwd).
func writeNodePasswd(t *testing.T, base, pw string) {
	t.Helper()
	hf, err := auth.NewHashFile([]byte(pw))
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.WriteHashFile(auth.HashFilePath(base), hf); err != nil {
		t.Fatal(err)
	}
}

// pwFile writes a --password-file and returns its path.
func pwFile(t *testing.T, pw string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(p, []byte(pw), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVerifierFor(t *testing.T) {
	sshLoc := locations.Location{Backend: locations.BackendSSH, Node: "n", User: "u", BasePath: "/v"}
	if _, ok := verifierFor(sshLoc, &backend.SSH{}).(sshVerifier); !ok {
		t.Error("ssh location should yield sshVerifier")
	}
	tdLoc := locations.Location{Backend: locations.BackendTaildrive, BasePath: "/mnt", Share: "s"}
	if _, ok := verifierFor(tdLoc, backend.NewTaildrive("/mnt")).(localVerifier); !ok {
		t.Error("taildrive location should yield localVerifier")
	}
}

func TestLocalVerifier(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	writeNodePasswd(t, base, "sesame")
	v := localVerifier{base: base}

	if ok, err := v.VerifyPassword(ctx, []byte("sesame")); err != nil || !ok {
		t.Errorf("correct: ok %v err %v; want true,nil", ok, err)
	}
	if ok, err := v.VerifyPassword(ctx, []byte("wrong")); err != nil || ok {
		t.Errorf("wrong: ok %v err %v; want false,nil", ok, err)
	}
	// No password set on the node.
	if _, err := (localVerifier{base: t.TempDir()}).VerifyPassword(ctx, []byte("x")); err == nil {
		t.Error("no-password-set should error (ErrNoPassword)")
	}
}

func TestGateLocation(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	writeNodePasswd(t, base, "open sesame")
	loc := locations.Location{Backend: locations.BackendTaildrive, BasePath: base, Share: "s"}
	b := backend.NewTaildrive(base)

	// Correct password via --password-file → nil.
	if err := gateLocation(ctx, loc, b, "home-pi", pwFile(t, "open sesame")); err != nil {
		t.Errorf("correct password: %v", err)
	}
	// Wrong password → TV-AUTH-01.
	if err := gateLocation(ctx, loc, b, "home-pi", pwFile(t, "nope")); !isTVCode(err, tserr.AuthRequired) {
		t.Errorf("wrong password: want TV-AUTH-01, got %v", err)
	}
	// No password set on the node → TV-AUTH-01 (actionable message).
	empty := locations.Location{Backend: locations.BackendTaildrive, BasePath: t.TempDir(), Share: "s"}
	err := gateLocation(ctx, empty, backend.NewTaildrive(empty.BasePath), "home-pi", pwFile(t, "x"))
	if !isTVCode(err, tserr.AuthRequired) {
		t.Errorf("no password set: want TV-AUTH-01, got %v", err)
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
