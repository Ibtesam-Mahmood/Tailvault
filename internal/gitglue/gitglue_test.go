package gitglue

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitRepo creates a temp git repo with an initial commit on branch "main".
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func commit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	full := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", file}, {"commit", "-m", msg}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestRepoRoot(t *testing.T) {
	dir := gitRepo(t)
	commit(t, dir, "f.txt", "hi", "init")
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := RepoRoot(sub)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	// macOS temp dirs resolve through /private; compare by EvalSymlinks.
	got, _ := filepath.EvalSymlinks(root)
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Errorf("RepoRoot = %s, want %s", got, want)
	}

	if _, err := RepoRoot(t.TempDir()); err == nil {
		t.Error("RepoRoot in a non-repo should error")
	}
}

func TestConfigSetGet(t *testing.T) {
	dir := gitRepo(t)
	if err := ConfigSet(dir, "filter.tailvault.clean", "tailvault filter-clean %f"); err != nil {
		t.Fatalf("ConfigSet: %v", err)
	}
	got, err := ConfigGet(dir, "filter.tailvault.clean")
	if err != nil {
		t.Fatalf("ConfigGet: %v", err)
	}
	if got != "tailvault filter-clean %f" {
		t.Errorf("ConfigGet = %q", got)
	}
	// Missing key -> empty, no error.
	v, err := ConfigGet(dir, "filter.tailvault.absent")
	if err != nil || v != "" {
		t.Errorf("absent key: got %q, %v; want empty, nil", v, err)
	}
}

func TestLocalBranches(t *testing.T) {
	dir := gitRepo(t)
	commit(t, dir, "f.txt", "hi", "init")
	if out, err := exec.Command("git", "-C", dir, "branch", "feature").CombinedOutput(); err != nil {
		t.Fatalf("branch: %s", out)
	}
	branches, err := LocalBranches(dir)
	if err != nil {
		t.Fatalf("LocalBranches: %v", err)
	}
	seen := map[string]bool{}
	for _, b := range branches {
		seen[b] = true
	}
	if !seen["main"] || !seen["feature"] {
		t.Errorf("branches = %v, want main + feature", branches)
	}
}

func TestReadFileAtRef(t *testing.T) {
	dir := gitRepo(t)
	commit(t, dir, "tailvault.lock", "version = 1\n", "add lock")

	content, found, err := ReadFileAtRef(dir, "main", "tailvault.lock")
	if err != nil || !found {
		t.Fatalf("ReadFileAtRef: found=%v err=%v", found, err)
	}
	if string(content) != "version = 1\n" {
		t.Errorf("content = %q", content)
	}

	// A path not committed at the ref -> found=false, no error.
	_, found, err = ReadFileAtRef(dir, "main", "nope.lock")
	if err != nil || found {
		t.Errorf("absent path: found=%v err=%v; want false, nil", found, err)
	}
}

func TestAddPath(t *testing.T) {
	dir := gitRepo(t)
	commit(t, dir, "seed.txt", "x", "seed")
	if err := os.WriteFile(filepath.Join(dir, "tailvault.lock"), []byte("v=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AddPath(dir, "tailvault.lock"); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	out, err := exec.Command("git", "-C", dir, "diff", "--cached", "--name-only").CombinedOutput()
	if err != nil {
		t.Fatalf("diff cached: %s", out)
	}
	if string(out) != "tailvault.lock\n" {
		t.Errorf("staged files = %q, want tailvault.lock", out)
	}
}
