package hooks

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates an empty git repo in a temp dir and returns its root.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

// writeStub writes an executable /bin/sh script and returns its path.
func writeStub(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
	return p
}

func TestInstallHooks_InstalledAndExecutable(t *testing.T) {
	repo := initRepo(t)
	bin := "/abs/path/to/tailvault"
	if err := InstallHooks(repo, bin); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	hooks := filepath.Join(repo, ".git", "hooks")
	for _, name := range hookNames {
		p := filepath.Join(hooks, name)
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("hook %s not installed: %v", name, err)
		}
		if fi.Mode()&0o111 == 0 {
			t.Errorf("hook %s is not executable (mode %v)", name, fi.Mode())
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(b), bin) {
			t.Errorf("hook %s does not embed absolute bin path %q:\n%s", name, bin, b)
		}
		if !strings.HasPrefix(string(b), "#!/bin/sh") {
			t.Errorf("hook %s missing /bin/sh shebang:\n%s", name, b)
		}
	}
}

func TestInstallHooks_PrePushForwardsExitCode(t *testing.T) {
	repo := initRepo(t)
	// Point binPath at a stub that always exits 4 (e.g. node unreachable).
	stub := writeStub(t, t.TempDir(), "tailvault", "#!/bin/sh\nexit 4\n")
	if err := InstallHooks(repo, stub); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	prePush := filepath.Join(repo, ".git", "hooks", "pre-push")
	err := exec.Command(prePush).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected pre-push to exit non-zero, got err=%v", err)
	}
	if got := ee.ExitCode(); got != 4 {
		t.Errorf("pre-push exit code = %d, want 4 (must forward tailvault push's code)", got)
	}
}

func TestInstallHooks_PullHooksInvokePull(t *testing.T) {
	repo := initRepo(t)
	stubDir := t.TempDir()
	argvLog := filepath.Join(stubDir, "argv.log")
	// Stub records its argv so we can confirm the hook invoked "pull".
	stub := writeStub(t, stubDir, "tailvault",
		"#!/bin/sh\necho \"$@\" >> "+argvLog+"\n")
	if err := InstallHooks(repo, stub); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	for _, name := range []string{"post-merge", "post-checkout"} {
		p := filepath.Join(repo, ".git", "hooks", name)
		if out, err := exec.Command(p).CombinedOutput(); err != nil {
			t.Fatalf("run %s: %v\n%s", name, err, out)
		}
	}
	got, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	lines := strings.Fields(strings.TrimSpace(string(got)))
	pulls := 0
	for _, l := range lines {
		if l == "pull" {
			pulls++
		}
	}
	if pulls != 2 {
		t.Errorf("expected post-merge and post-checkout each to invoke tailvault pull (2 total), got %d in %q", pulls, got)
	}
}

func TestInstallHooks_Idempotent(t *testing.T) {
	repo := initRepo(t)
	bin := "/abs/path/tailvault"
	if err := InstallHooks(repo, bin); err != nil {
		t.Fatalf("first InstallHooks: %v", err)
	}
	hooks := filepath.Join(repo, ".git", "hooks")
	first := map[string][]byte{}
	for _, name := range hookNames {
		b, err := os.ReadFile(filepath.Join(hooks, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		first[name] = b
	}
	if err := InstallHooks(repo, bin); err != nil {
		t.Fatalf("second InstallHooks: %v", err)
	}
	for _, name := range hookNames {
		p := filepath.Join(hooks, name)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("re-read %s: %v", name, err)
		}
		if !bytes.Equal(first[name], b) {
			t.Errorf("hook %s changed across idempotent re-install", name)
		}
		fi, _ := os.Stat(p)
		if fi.Mode()&0o111 == 0 {
			t.Errorf("hook %s lost exec bit after re-install", name)
		}
	}
}

func TestInstallHooks_WarnsOnForeignHook(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	// A pre-existing non-tailvault pre-push hook.
	foreign := filepath.Join(hooks, "pre-push")
	if err := os.WriteFile(foreign, []byte("#!/bin/sh\necho custom\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	if err := installHooks(repo, "/abs/tailvault", &warn); err != nil {
		t.Fatalf("installHooks: %v", err)
	}
	if !strings.Contains(warn.String(), "pre-push") {
		t.Errorf("expected a warning about overwriting the foreign pre-push hook, got %q", warn.String())
	}
	// And it is now tailvault-managed (sentinel present).
	b, _ := os.ReadFile(foreign)
	if !strings.Contains(string(b), sentinel) {
		t.Errorf("foreign hook not replaced with tailvault-managed hook:\n%s", b)
	}
	// A second install no longer warns (it is now ours).
	warn.Reset()
	if err := installHooks(repo, "/abs/tailvault", &warn); err != nil {
		t.Fatalf("second installHooks: %v", err)
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warning re-installing our own hook: %q", warn.String())
	}
}

func TestInstallHooks_EmptyBinPath(t *testing.T) {
	repo := initRepo(t)
	if err := InstallHooks(repo, ""); err == nil {
		t.Error("expected error for empty bin path")
	}
}
