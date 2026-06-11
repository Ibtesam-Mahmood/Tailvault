package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/gitglue"
	"github.com/Ibtesam-Mahmood/tailvault/internal/hooks"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// Git config keys/values the filter driver and merge driver are registered
// under. The commands they point at are tailvault subcommands invoked by git.
var filterConfig = [][2]string{
	{"filter.tailvault.clean", "tailvault filter-clean %f"},
	{"filter.tailvault.smudge", "tailvault filter-smudge %f"},
	{"filter.tailvault.required", "true"},
	{"merge.tailvault.name", "tailvault lock per-path union merge"},
	{"merge.tailvault.driver", "tailvault __merge-lock %O %A %B"},
}

// defaultAttributes are the .gitattributes lines init ensures are present. The
// extension lines route the locked default file types through the filter; the
// last line registers the per-path union merge driver for the committed lock.
var defaultAttributes = []string{
	"*.pdf filter=tailvault -text",
	"*.stl filter=tailvault -text",
	"*.3mf filter=tailvault -text",
	"*.pptx filter=tailvault -text",
	"tailvault.lock merge=tailvault",
}

func newInitCmd() *cobra.Command {
	var location string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write tailvault.toml + .gitattributes and install hooks",
		Long: "Bootstrap a git repo for tailvault (non-interactive). Writes a default " +
			"tailvault.toml, registers the clean/smudge filter and the lock merge driver " +
			"in .gitattributes + git config, and installs the git hooks. Idempotent: " +
			"re-running changes nothing and never clobbers an existing tailvault.toml.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd, location)
		},
	}
	cmd.Flags().StringVar(&location, "location", "", "storage location name to prefill in tailvault.toml")
	return cmd
}

func runInit(cmd *cobra.Command, location string) error {
	// init is local-only: no node preflight. It must run from anywhere inside
	// the repo, so resolve the root first.
	repoRoot, err := gitglue.RepoRoot("")
	if err != nil {
		return tserr.ConfigErr("tailvault init must run inside a git repository", err)
	}
	out := cmd.OutOrStdout()

	// 1. Default tailvault.toml if absent; never overwrite a user's edits.
	cfgPath := filepath.Join(repoRoot, "tailvault.toml")
	if _, statErr := os.Stat(cfgPath); statErr == nil {
		fmt.Fprintln(out, "tailvault.toml already exists — leaving it untouched")
	} else if os.IsNotExist(statErr) {
		cfg := config.Default()
		cfg.Storage.Location = location // may be empty; user sets it via setup/location add
		if err := config.Write(cfgPath, &cfg); err != nil {
			return tserr.ConfigErr("init: write tailvault.toml", err)
		}
		fmt.Fprintln(out, "wrote tailvault.toml")
	} else {
		return tserr.ConfigErr("init: stat tailvault.toml", statErr)
	}

	// 2. Register filter + merge attribute lines (idempotent, append-only).
	added, err := ensureAttributes(filepath.Join(repoRoot, ".gitattributes"), defaultAttributes)
	if err != nil {
		return fmt.Errorf("init: update .gitattributes: %w", err)
	}
	fmt.Fprintf(out, ".gitattributes: %d line(s) added\n", added)

	// 3. git config for the filter + merge drivers (re-setting is idempotent).
	for _, kv := range filterConfig {
		if err := gitglue.ConfigSet(repoRoot, kv[0], kv[1]); err != nil {
			return fmt.Errorf("init: %w", err)
		}
	}

	// 4. Install hooks, invoking tailvault by absolute path.
	bin, err := os.Executable()
	if err != nil || bin == "" {
		bin = "tailvault" // fall back to PATH lookup
	}
	if err := hooks.InstallHooks(repoRoot, bin); err != nil {
		return fmt.Errorf("init: install hooks: %w", err)
	}
	fmt.Fprintln(out, "installed git hooks (pre-push, post-merge, post-checkout)")
	return nil
}

// ensureAttributes appends each wanted line to .gitattributes that is not
// already present (exact match after trimming), creating the file if needed. It
// returns how many lines were added. Existing content and ordering are
// preserved, so re-running init never duplicates a line.
func ensureAttributes(path string, want []string) (int, error) {
	var existing []string
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	present := make(map[string]struct{})
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			t := strings.TrimSpace(line)
			if t != "" {
				present[t] = struct{}{}
			}
			existing = append(existing, line)
		}
	}

	var toAdd []string
	for _, w := range want {
		if _, ok := present[strings.TrimSpace(w)]; !ok {
			toAdd = append(toAdd, w)
		}
	}
	if len(toAdd) == 0 {
		return 0, nil
	}

	// Build new content: keep existing bytes, ensure a trailing newline, append.
	var b strings.Builder
	if len(data) > 0 {
		b.Write(data)
		if !strings.HasSuffix(string(data), "\n") {
			b.WriteString("\n")
		}
	}
	for _, l := range toAdd {
		b.WriteString(l)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return 0, err
	}
	return len(toAdd), nil
}
