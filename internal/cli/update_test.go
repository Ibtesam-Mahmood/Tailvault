package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/update"
	"github.com/Ibtesam-Mahmood/tailvault/internal/version"
)

type fakeUpdateFetcher struct {
	rel update.Release
	err error
}

func (f fakeUpdateFetcher) Latest(context.Context) (update.Release, error) { return f.rel, f.err }

// runWithStdin executes the root command with args, feeding stdin, capturing
// stdout+stderr.
func runWithStdin(stdin string, args ...string) (string, error) {
	c := newRootCmd()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetIn(strings.NewReader(stdin))
	c.SetArgs(args)
	err := c.Execute()
	return buf.String(), err
}

func TestUpdateCheckReportsNewer(t *testing.T) {
	origV := version.Version
	origF := updateFetcher
	t.Cleanup(func() { version.Version = origV; updateFetcher = origF })

	version.Version = "0.0.105"
	updateFetcher = func() update.Fetcher {
		return fakeUpdateFetcher{rel: update.Release{Tag: "v0.0.106"}}
	}

	out, err := run("update", "--check")
	if err != nil {
		t.Fatalf("update --check: %v", err)
	}
	if !strings.Contains(out, "0.0.106") || !strings.Contains(out, "tailvault update") {
		t.Errorf("expected an upgrade hint mentioning 0.0.106, got %q", out)
	}
}

func TestUpdateCheckUpToDate(t *testing.T) {
	origV := version.Version
	origF := updateFetcher
	t.Cleanup(func() { version.Version = origV; updateFetcher = origF })

	version.Version = "0.0.106"
	updateFetcher = func() update.Fetcher {
		return fakeUpdateFetcher{rel: update.Release{Tag: "v0.0.106"}}
	}

	out, err := run("update", "--check")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "latest") {
		t.Errorf("expected an up-to-date message, got %q", out)
	}
}

func TestUninstallCancelRemovesNothing(t *testing.T) {
	// Point state/config dirs at temp locations and populate them.
	home := t.TempDir()
	cfg := t.TempDir()
	stateDir := filepath.Join(home, ".tailvault")
	cfgDir := filepath.Join(cfg, "tailvault")
	t.Setenv("TAILVAULT_HOME", stateDir)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Answer "n" at the confirm prompt → nothing is removed.
	out, err := runWithStdin("n\n", "update", "--uninstall")
	if err != nil {
		t.Fatalf("uninstall (cancel): %v", err)
	}
	if !strings.Contains(out, "cancelled") {
		t.Errorf("expected a cancellation message, got %q", out)
	}
	if _, err := os.Stat(stateDir); err != nil {
		t.Errorf("state dir should survive a cancelled uninstall: %v", err)
	}
	if _, err := os.Stat(cfgDir); err != nil {
		t.Errorf("config dir should survive a cancelled uninstall: %v", err)
	}
	// The listing should still name what *would* be removed.
	if !strings.Contains(out, "node registry") || !strings.Contains(out, "left untouched") {
		t.Errorf("expected a target listing with the safety note, got %q", out)
	}
}
