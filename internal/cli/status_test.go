package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// TestStatus_BadConfig_IsTVCFGExit2 proves the command boundary wraps a config
// load/parse failure as TV-CFG-01 (exit bucket 2) per SPEC §5 / team-lead mandate.
func TestStatus_BadConfig_IsTVCFGExit2(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, configName), "this is not valid toml = = =\n")
	cwd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(cwd) })
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}

	r := newRootCmd()
	r.SetOut(&bytes.Buffer{})
	r.SetErr(&bytes.Buffer{})
	r.SetArgs([]string{"status"})
	err := r.Execute()
	if err == nil {
		t.Fatal("want a config error for malformed tailvault.toml")
	}
	var te *tserr.Error
	if !errors.As(err, &te) || te.ExitCode() != 2 {
		t.Errorf("want TV-CFG exit 2, got %v (ExitCodeFor=%d)", err, tserr.ExitCodeFor(err))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStatus_Offline builds a temp repo with one file in each state and asserts
// the table — no node contact (default offline path).
func TestStatus_Offline(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, configName), "version = 1\n[storage]\nlocation = \"home-pi\"\n[rules]\ninclude = [\"**/*.pdf\"]\n")

	// pushed.pdf: content hashed below; lock will carry its sha.
	writeFile(t, filepath.Join(root, "pushed.pdf"), "pushed-content")
	writeFile(t, filepath.Join(root, "drifted.pdf"), "new-content")
	writeFile(t, filepath.Join(root, "local.pdf"), "brand-new")

	// Compute the pushed/drifted shas via ScanTree by reusing the engine.
	// Simpler: hash through the same helper the command uses indirectly.
	pushedSHA := shaOf(t, filepath.Join(root, "pushed.pdf"))

	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{
		{Path: "pushed.pdf", SHA256: pushedSHA, Size: 14, Location: "home-pi"},
		{Path: "drifted.pdf", SHA256: "0000deadbeef", Size: 1, Location: "home-pi"},
		{Path: "gone.pdf", SHA256: "1111", Size: 1, Location: "home-pi"},
	}}
	if err := lock.Write(filepath.Join(root, lockName), lk, "test"); err != nil {
		t.Fatalf("lock.Write: %v", err)
	}

	cwd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(cwd) })
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}

	root2 := newRootCmd()
	var out bytes.Buffer
	root2.SetOut(&out)
	root2.SetErr(&out)
	root2.SetArgs([]string{"status"})
	if err := root2.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	s := out.String()
	checks := map[string]string{
		"pushed.pdf":  "pushed",
		"drifted.pdf": "drifted",
		"local.pdf":   "local-only",
		"gone.pdf":    "orphaned",
	}
	for path, state := range checks {
		if !lineHas(s, path, state) {
			t.Errorf("status table missing %q as %q:\n%s", path, state, s)
		}
	}
}

func TestStatBlobs_PresenceViaStub(t *testing.T) {
	ctx := context.Background()
	b := backend.NewFSBackend(t.TempDir())
	// Put one blob; leave another absent.
	_ = b.Put(ctx, "objects/aaaa", bytes.NewReader([]byte("x")))

	tree := map[string]string{"present.pdf": "aaaa", "missing.pdf": "bbbb"}
	locked := map[string]lock.Entry{
		"present.pdf": {Path: "present.pdf", SHA256: "aaaa"},
		"missing.pdf": {Path: "missing.pdf", SHA256: "bbbb"},
	}
	got, err := statBlobs(ctx, b, tree, locked)
	if err != nil {
		t.Fatalf("statBlobs: %v", err)
	}
	if !got["aaaa"] {
		t.Errorf("aaaa should be present")
	}
	if got["bbbb"] {
		t.Errorf("bbbb should be absent")
	}
}

func shaOf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func lineHas(out, a, b string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, a) && strings.Contains(line, b) {
			return true
		}
	}
	return false
}
