package cli

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/status"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// configFile is the repo-committed project config filename.
const configFile = "tailvault.toml"

func newTrackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "track <glob>...",
		Short: "Add include rule(s) to tailvault.toml",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTrack(cmd, ".", args)
		},
	}
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
