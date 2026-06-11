package cli

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/status"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// configFile is the repo-committed project config filename.
const configFile = "tailvault.toml"

func newTrackCmd() *cobra.Command {
	var vaultMode, repoMode bool
	cmd := &cobra.Command{
		Use:   "track <glob>... | <location>/<path>",
		Short: "Track files: a repo include-rule (in a repo), or vault registration of an existing file",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vault, err := trackIsVaultMode(args, vaultMode, repoMode)
			if err != nil {
				return err
			}
			if vault {
				if len(args) != 1 {
					return tserr.ConfigErr("track: vault-mode takes exactly one <location>/<path|glob>", nil)
				}
				return runTrackVault(cmd, args[0])
			}
			return runTrack(cmd, ".", args)
		},
	}
	cmd.Flags().BoolVar(&vaultMode, "vault", false, "force vault-mode: register an existing file at <location>/<path>")
	cmd.Flags().BoolVar(&repoMode, "repo", false, "force repo-mode: add an include rule to tailvault.toml")
	return cmd
}

// trackIsVaultMode decides repo-mode vs vault-mode for `track`. Forcing flags win
// (both → error). Otherwise: a single arg whose first segment is a REGISTERED
// location name (containing '/') is vault-mode when not inside a repo; if it is
// ALSO a plausible repo glob (inside a repo) it is ambiguous and the user must
// pass --vault/--repo (never silently turn a repo glob into a vault op).
// Everything else (multiple args, no location prefix) stays repo-mode —
// preserving Block 1 behavior byte-for-byte.
func trackIsVaultMode(args []string, forceVault, forceRepo bool) (bool, error) {
	if forceVault && forceRepo {
		return false, tserr.ConfigErr("track: pass only one of --vault / --repo", nil)
	}
	if forceVault {
		return true, nil
	}
	if forceRepo {
		return false, nil
	}
	if len(args) != 1 || !strings.Contains(args[0], "/") {
		return false, nil
	}
	locName := args[0][:strings.IndexByte(args[0], '/')]
	if !isRegisteredLocation(locName) {
		return false, nil
	}
	if _, err := findRepoRoot(); err == nil {
		return false, tserr.ConfigErr(
			fmt.Sprintf("track: %q is ambiguous (a repo glob or vault path %q) — pass --repo or --vault", args[0], locName), nil)
	}
	return true, nil
}

// isRegisteredLocation reports whether name is a location in the user registry.
func isRegisteredLocation(name string) bool {
	reg, err := locations.Load()
	if err != nil {
		return false
	}
	_, ok := reg.Locations[name]
	return ok
}

// runTrack validates the globs, appends the new ones to the config's include
// list (idempotently), writes the config only if it changed, then re-runs the
// rule engine over the tree rooted at root and reports the currently-managed
// files. It never contacts a storage node.
func runTrack(cmd *cobra.Command, root string, globs []string) error {
	out := cmd.OutOrStdout()
	cfgPath := filepath.Join(root, configFile)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return tserr.ConfigErr(fmt.Sprintf("load %s", cfgPath), err)
	}

	// Validate ALL globs before mutating, so a bad glob never corrupts config.
	for _, g := range globs {
		if err := config.ValidateGlob(g); err != nil {
			return tserr.ConfigErr(err.Error(), err)
		}
	}

	changed := false
	for _, g := range globs {
		if cfg.AddInclude(g) {
			changed = true
			fmt.Fprintf(out, "tracking %s\n", g)
		} else {
			fmt.Fprintf(out, "already tracked: %s\n", g)
		}
	}
	if changed {
		if err := config.Write(cfgPath, cfg); err != nil {
			return tserr.ConfigErr(fmt.Sprintf("write %s", cfgPath), err)
		}
	}

	// Reuse status.ManagedFiles (pointer-aware via status.ContentSize) so a file
	// managed only by min_size that is currently a clean pointer is still
	// reported — matching exactly what `status`/`push` consider managed.
	matches, err := status.ManagedFiles(cfg, root)
	if err != nil {
		return tserr.ConfigErr("scan working tree", err)
	}
	sort.Strings(matches)
	fmt.Fprintln(out, "matches:")
	for _, p := range matches {
		fmt.Fprintf(out, "  %s\n", p)
	}
	return nil
}
