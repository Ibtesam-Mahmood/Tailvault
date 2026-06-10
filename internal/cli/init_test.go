package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

// runInitIn executes `tailvault init` (with optional args) inside dir.
func runInitIn(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	t.Chdir(dir)
	cmd := newInitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestInit_Fresh(t *testing.T) {
	dir := gitInit(t)
	if _, err := runInitIn(t, dir); err != nil {
		t.Fatalf("init: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "tailvault.toml")); err != nil {
		t.Errorf("tailvault.toml not written: %v", err)
	}
	attrs, err := os.ReadFile(filepath.Join(dir, ".gitattributes"))
	if err != nil {
		t.Fatalf("read .gitattributes: %v", err)
	}
	if !strings.Contains(string(attrs), "filter=tailvault") {
		t.Errorf(".gitattributes missing filter line:\n%s", attrs)
	}
	if !strings.Contains(string(attrs), "tailvault.lock merge=tailvault") {
		t.Errorf(".gitattributes missing merge driver line:\n%s", attrs)
	}
	// git config keys set.
	for _, kv := range filterConfig {
		out, err := exec.Command("git", "-C", dir, "config", "--get", kv[0]).Output()
		if err != nil {
			t.Errorf("git config %s not set: %v", kv[0], err)
			continue
		}
		if strings.TrimSpace(string(out)) != kv[1] {
			t.Errorf("git config %s = %q, want %q", kv[0], strings.TrimSpace(string(out)), kv[1])
		}
	}
	// hooks installed.
	for _, h := range []string{"pre-push", "post-merge", "post-checkout"} {
		if _, err := os.Stat(filepath.Join(dir, ".git", "hooks", h)); err != nil {
			t.Errorf("hook %s not installed: %v", h, err)
		}
	}
}

func TestInit_Idempotent(t *testing.T) {
	dir := gitInit(t)
	if _, err := runInitIn(t, dir); err != nil {
		t.Fatalf("first init: %v", err)
	}
	attrsPath := filepath.Join(dir, ".gitattributes")
	tomlPath := filepath.Join(dir, "tailvault.toml")
	attrs1, _ := os.ReadFile(attrsPath)
	toml1, _ := os.ReadFile(tomlPath)

	if _, err := runInitIn(t, dir); err != nil {
		t.Fatalf("second init: %v", err)
	}
	attrs2, _ := os.ReadFile(attrsPath)
	toml2, _ := os.ReadFile(tomlPath)

	if !bytes.Equal(attrs1, attrs2) {
		t.Errorf(".gitattributes changed on re-init:\n%s\n---\n%s", attrs1, attrs2)
	}
	if !bytes.Equal(toml1, toml2) {
		t.Errorf("tailvault.toml changed on re-init")
	}
	// No duplicate attribute lines.
	if c := strings.Count(string(attrs2), "tailvault.lock merge=tailvault"); c != 1 {
		t.Errorf("merge line appears %d times, want 1", c)
	}
}

func TestInit_ExistingConfigPreserved(t *testing.T) {
	dir := gitInit(t)
	custom := "version = 1\n[storage]\nlocation = \"home-pi\"\n[rules]\nmin_size = \"99MB\"\n"
	if err := os.WriteFile(filepath.Join(dir, "tailvault.toml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runInitIn(t, dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "tailvault.toml"))
	if string(got) != custom {
		t.Errorf("existing tailvault.toml was overwritten:\n%s", got)
	}
}

func TestInit_NotAGitRepo(t *testing.T) {
	dir := t.TempDir() // not a git repo
	_, err := runInitIn(t, dir)
	if err == nil {
		t.Fatal("expected error running init outside a git repo")
	}
}
