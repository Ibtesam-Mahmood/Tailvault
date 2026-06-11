package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/auth"
	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// loadNodeHash reads + parses the node's PHC hash file.
func loadNodeHash(t *testing.T, dir string) auth.HashFile {
	t.Helper()
	hf, ok, err := auth.LoadHashFile(filepath.Join(dir, filepath.FromSlash(auth.HashFileRel)))
	if err != nil || !ok {
		t.Fatalf("load hash file: ok=%v err=%v", ok, err)
	}
	return hf
}

func TestVaultPasswd_FirstSet(t *testing.T) {
	dir := registerVault(t, "home-pi", nil)
	pw := pwFile(t, "correct horse")

	out, err := run("vault", "passwd", "home-pi", "--new-password-file", pw)
	if err != nil {
		t.Fatalf("passwd set: %v\n%s", err, out)
	}
	// Hash file written, parses, and verifies the password; not the wrong one.
	hf := loadNodeHash(t, dir)
	if !auth.Verify(hf, []byte("correct horse")) {
		t.Error("hash must verify the set password")
	}
	if auth.Verify(hf, []byte("wrong")) {
		t.Error("hash must reject a wrong password")
	}
	// 0600 on the secret.
	fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(auth.HashFileRel)))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("hash file mode = %o, want 0600", fi.Mode().Perm())
	}
	// Output mentions the no-recovery reset path.
	if !strings.Contains(out, "no recovery") {
		t.Errorf("output must document the no-recovery reset path:\n%s", out)
	}
	// WAL: a done passwd op.
	recs, _ := (&wal.Log{B: backend.NewTaildrive(dir)}).Read(context.Background())
	found := false
	for _, r := range recs {
		if r.Entry.OpType == wal.OpPasswd && r.State == wal.StateDone && r.Entry.Args["action"] == "set" {
			found = true
		}
	}
	if !found {
		t.Error("missing done passwd WAL record (action=set)")
	}
}

func TestVaultPasswd_Change(t *testing.T) {
	dir := registerVault(t, "home-pi", nil)
	if _, err := run("vault", "passwd", "home-pi", "--new-password-file", pwFile(t, "old-secret")); err != nil {
		t.Fatalf("initial set: %v", err)
	}
	// Change to a new password. (On taildrive the old-password gate is a no-op per
	// DEV-46.8; the SSH old-password verification is covered by vault_gate_test.go.)
	out, err := run("vault", "passwd", "home-pi", "--password-file", pwFile(t, "old-secret"), "--new-password-file", pwFile(t, "new-secret"))
	if err != nil {
		t.Fatalf("passwd change: %v\n%s", err, out)
	}
	hf := loadNodeHash(t, dir)
	if !auth.Verify(hf, []byte("new-secret")) || auth.Verify(hf, []byte("old-secret")) {
		t.Error("after a change the hash must verify the NEW password and reject the old")
	}
	recs, _ := (&wal.Log{B: backend.NewTaildrive(dir)}).Read(context.Background())
	changes := 0
	for _, r := range recs {
		if r.Entry.OpType == wal.OpPasswd && r.Entry.Args["action"] == "change" {
			changes++
		}
	}
	if changes != 1 {
		t.Errorf("want exactly one change WAL record, got %d", changes)
	}
}

func TestVaultPasswd_EmptyRejected(t *testing.T) {
	dir := registerVault(t, "home-pi", nil)
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil { // a lone newline → empty after strip
		t.Fatal(err)
	}
	if _, err := run("vault", "passwd", "home-pi", "--new-password-file", empty); !isTVCode(err, tserr.ConfigBad) {
		t.Fatalf("empty password: want TV-CFG-01, got %v", err)
	}
	// No hash file written.
	if _, ok, _ := auth.LoadHashFile(filepath.Join(dir, filepath.FromSlash(auth.HashFileRel))); ok {
		t.Error("a rejected empty password must not write a hash file")
	}
}
