//go:build integration

package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/push"
	"github.com/Ibtesam-Mahmood/tailvault/internal/verify"
)

// sshLocalhost returns the login user and true when passwordless `ssh localhost`
// works, so the SSH-backed scenario can run; otherwise the caller t.Skips. This
// keeps CI green on runners without localhost SSH (the taildrive half always
// runs).
func sshLocalhost(t *testing.T) (string, bool) {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		return "", false
	}
	user := os.Getenv("USER")
	if user == "" {
		return "", false
	}
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=no", "localhost", "true")
	if err := cmd.Run(); err != nil {
		return "", false
	}
	return user, true
}

// TestSSH_PushVerifyRoundTrip exercises the real SSH backend against a loopback
// `ssh localhost` writing into a temp "node" dir: push uploads a blob, verify
// confirms integrity. Mirrors the taildrive scenarios over the other transport.
func TestSSH_PushVerifyRoundTrip(t *testing.T) {
	user, ok := sshLocalhost(t)
	if !ok {
		t.Skip("no usable passwordless ssh to localhost; SSH transport scenario skipped")
	}
	ctx := context.Background()
	root := t.TempDir()
	node := t.TempDir()
	git(t, root, "init")
	git(t, root, "config", "user.email", "tester@example.com")
	git(t, root, "config", "user.name", "tester")

	be := &backend.SSH{
		User:     user,
		Node:     "localhost",
		BasePath: node,
		Ping:     func(context.Context, string) error { return nil },
	}
	cfg := &config.Config{
		Version: 1,
		Storage: config.Storage{Location: "home-pi"},
		Rules:   config.Rules{MinSize: "5MB", Include: []string{"**/*.bin"}, AutoDelete: true},
	}

	if err := os.WriteFile(filepath.Join(root, "a.bin"), []byte("ssh-payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := push.Deps{
		Backend:     be,
		Preflight:   func(context.Context) error { return nil },
		Whois:       func(context.Context) (string, error) { return "tester@laptop", nil },
		GitIdentity: func() string { return "tester@example.com" },
		Now:         func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
	if _, err := push.Run(ctx, root, cfg, &lock.Lock{Version: 1}, deps, push.Options{}); err != nil {
		t.Fatalf("ssh push: %v", err)
	}

	sha := shaOf("ssh-payload")
	if m, err := be.Stat(ctx, "objects/"+sha); err != nil || !m.Exists {
		t.Fatalf("blob not present on ssh node: exists=%v err=%v", m.Exists, err)
	}
	lk, err := lock.Load(filepath.Join(root, "tailvault.lock"))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := verify.Run(ctx, be, lk)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		t.Errorf("verify over ssh: corrupt=%v missing=%v, want clean", rep.Corrupt, rep.Missing)
	}
}
