package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/pointer"
)

// trackRepo builds a temp repo with a minimal tailvault.toml (min_size 1KB to
// keep file I/O small) and a few files of varying size/extension.
func trackRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	toml := "version = 1\n\n[storage]\nlocation = \"home-pi\"\n\n[rules]\nmin_size = \"1KB\"\nexclude = [\"drafts/**\"]\n"
	if err := os.WriteFile(filepath.Join(dir, configFile), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(rel string, n int) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.pdf", 2048)        // >= min_size and .pdf → match
	write("b.txt", 100)         // small, not included → no match
	write("small.pdf", 100)     // < min_size but .pdf include match → match (union rule)
	write("drafts/x.pdf", 4096) // excluded by drafts/** → no match
	return dir
}

func runTrackT(t *testing.T, root string, globs ...string) (string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := runTrack(cmd, root, globs)
	return buf.String(), err
}

func TestTrackAddsAndReports(t *testing.T) {
	dir := trackRepo(t)
	out, err := runTrackT(t, dir, "**/*.pdf")
	if err != nil {
		t.Fatalf("runTrack: %v", err)
	}
	if !strings.Contains(out, "tracking **/*.pdf") {
		t.Errorf("missing tracking header:\n%s", out)
	}
	// Per the rule engine (task-05) + proposal, managed = (size>=min_size OR
	// include) AND NOT exclude. So both the large a.pdf and the tiny small.pdf
	// match via the include glob; only the non-pdf and the excluded file drop out.
	for _, yes := range []string{"a.pdf", "small.pdf"} {
		if !strings.Contains(out, yes) {
			t.Errorf("%s should be reported (include match):\n%s", yes, out)
		}
	}
	for _, no := range []string{"b.txt", "drafts/x.pdf"} {
		if strings.Contains(out, no) {
			t.Errorf("%s should NOT be in matches:\n%s", no, out)
		}
	}
	// Config persisted with the new include.
	cfg, err := config.Load(filepath.Join(dir, configFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rules.Include) != 1 || cfg.Rules.Include[0] != "**/*.pdf" {
		t.Errorf("include not persisted: %v", cfg.Rules.Include)
	}
	// Unrelated section preserved.
	if cfg.Storage.Location != "home-pi" {
		t.Errorf("[storage] wiped: %q", cfg.Storage.Location)
	}
}

func TestTrackIdempotent(t *testing.T) {
	dir := trackRepo(t)
	if _, err := runTrackT(t, dir, "**/*.pdf"); err != nil {
		t.Fatal(err)
	}
	out, err := runTrackT(t, dir, "**/*.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "already tracked: **/*.pdf") {
		t.Errorf("expected already-tracked message:\n%s", out)
	}
	cfg, _ := config.Load(filepath.Join(dir, configFile))
	if len(cfg.Rules.Include) != 1 {
		t.Errorf("include duplicated: %v", cfg.Rules.Include)
	}
}

func TestTrackInvalidGlobLeavesConfigUntouched(t *testing.T) {
	dir := trackRepo(t)
	before, _ := os.ReadFile(filepath.Join(dir, configFile))
	_, err := runTrackT(t, dir, "[bad")
	if err == nil {
		t.Fatal("invalid glob should error")
	}
	after, _ := os.ReadFile(filepath.Join(dir, configFile))
	if string(before) != string(after) {
		t.Error("config must be untouched when a glob is invalid")
	}
}

func TestTrackMultipleGlobsValidateBeforeMutating(t *testing.T) {
	dir := trackRepo(t)
	before, _ := os.ReadFile(filepath.Join(dir, configFile))
	// One good, one bad → nothing should be written.
	if _, err := runTrackT(t, dir, "**/*.stl", "[bad"); err == nil {
		t.Fatal("a bad glob in the batch should fail the whole command")
	}
	after, _ := os.ReadFile(filepath.Join(dir, configFile))
	if string(before) != string(after) {
		t.Error("no glob should be applied if any is invalid")
	}
	// Both good → both appended.
	if _, err := runTrackT(t, dir, "**/*.pdf", "**/*.stl"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(filepath.Join(dir, configFile))
	if len(cfg.Rules.Include) != 2 {
		t.Errorf("both globs should be appended: %v", cfg.Rules.Include)
	}
}

func TestTrackMissingConfigIsConfigError(t *testing.T) {
	dir := t.TempDir() // no tailvault.toml
	_, err := runTrackT(t, dir, "**/*.pdf")
	if err == nil {
		t.Fatal("missing config should error")
	}
}

// TestTrackReportsPointerizedMinSizeFile is the regression test for the R-A LOW
// finding: a file managed only by min_size that is currently a clean pointer
// (~60B on disk) must still be reported. Reusing status.ManagedFiles makes the
// match pointer-aware (ContentSize reads the pointer's real size) where the old
// local walk used the on-disk size and wrongly dropped it.
func TestTrackReportsPointerizedMinSizeFile(t *testing.T) {
	dir := trackRepo(t)
	// blob.bin matches NO include glob; it's managed solely by min_size (1KB).
	// On disk it's a clean pointer whose recorded content size is 4096 (>1KB).
	ptr := pointer.Encode(pointer.Pointer{SHA256: "deadbeef", Size: 4096, Location: "home-pi"})
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), ptr, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runTrackT(t, dir, "**/*.pdf") // tracked glob is unrelated to .bin
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "blob.bin") {
		t.Errorf("pointerized min_size-only file should be reported as managed:\n%s", out)
	}
}
