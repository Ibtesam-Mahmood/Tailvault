package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
)

func TestMergeLock_Unit(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base")
	ours := filepath.Join(dir, "ours")
	theirs := filepath.Join(dir, "theirs")

	mustWrite := func(p string, entries []lock.Entry) {
		if err := lock.Write(p, &lock.Lock{Entries: entries}, "tailvault test"); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	mustWrite(base, []lock.Entry{{Path: "shared.pdf", SHA256: "S", Location: "home-pi"}})
	mustWrite(ours, []lock.Entry{
		{Path: "shared.pdf", SHA256: "S", Location: "home-pi"},
		{Path: "only-ours.pdf", SHA256: "O", Location: "home-pi"},
	})
	mustWrite(theirs, []lock.Entry{
		{Path: "shared.pdf", SHA256: "S", Location: "home-pi"},
		{Path: "only-theirs.pdf", SHA256: "T", Location: "home-pi"},
	})

	cmd := newMergeLockCmd()
	cmd.SetArgs([]string{base, ours, theirs})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("__merge-lock: %v", err)
	}

	merged, err := lock.Load(ours) // driver writes the result back into %A (ours)
	if err != nil {
		t.Fatalf("load merged: %v", err)
	}
	for _, p := range []string{"shared.pdf", "only-ours.pdf", "only-theirs.pdf"} {
		if _, ok := merged.Find(p); !ok {
			t.Errorf("merged lock missing %q: %+v", p, merged.Entries)
		}
	}
}

// TestMergeLock_GitIntegration builds the binary, registers the merge driver,
// and drives a real `git merge` of two branches that edit the lock for disjoint
// paths, asserting the union merges cleanly without manual resolution.
func TestMergeLock_GitIntegration(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "tailvault")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/Ibtesam-Mahmood/tailvault/cmd/tailvault").CombinedOutput(); err != nil {
		t.Skipf("cannot build binary for integration test: %v\n%s", err, out)
	}

	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-b", "main")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")
	// Register the driver with the absolute binary path (production uses PATH).
	git("config", "merge.tailvault.name", "tailvault lock per-path union merge")
	git("config", "merge.tailvault.driver", bin+" __merge-lock %O %A %B")

	writeLock := func(entries []lock.Entry) {
		if err := lock.Write(filepath.Join(repo, "tailvault.lock"), &lock.Lock{Entries: entries}, "tailvault test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("tailvault.lock merge=tailvault\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	shared := lock.Entry{Path: "shared.pdf", SHA256: "S", Location: "home-pi"}
	writeLock([]lock.Entry{shared})
	git("add", ".")
	git("commit", "-m", "base")

	// Branch feature off the base.
	git("branch", "feature")

	// main adds only-main.pdf.
	writeLock([]lock.Entry{shared, {Path: "only-main.pdf", SHA256: "M", Location: "home-pi"}})
	git("add", "tailvault.lock")
	git("commit", "-m", "main edit")

	// feature adds only-feature.pdf.
	git("checkout", "feature")
	writeLock([]lock.Entry{shared, {Path: "only-feature.pdf", SHA256: "F", Location: "home-pi"}})
	git("add", "tailvault.lock")
	git("commit", "-m", "feature edit")

	// Merge feature into main — the driver must union both edits with no conflict.
	git("checkout", "main")
	out, err := exec.Command("git", "-C", repo, "merge", "--no-edit", "feature").CombinedOutput()
	if err != nil {
		t.Fatalf("git merge failed (driver should auto-resolve): %v\n%s", err, out)
	}

	merged, err := lock.Load(filepath.Join(repo, "tailvault.lock"))
	if err != nil {
		t.Fatalf("load merged lock: %v", err)
	}
	for _, p := range []string{"shared.pdf", "only-main.pdf", "only-feature.pdf"} {
		if _, ok := merged.Find(p); !ok {
			t.Errorf("merged lock missing %q after git merge: %+v", p, merged.Entries)
		}
	}
}
