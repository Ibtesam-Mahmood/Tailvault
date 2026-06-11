package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/gc"
	"github.com/Ibtesam-Mahmood/tailvault/internal/gitglue"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

func newGCCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Prune unreferenced blobs per retention policy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := findRepoRoot()
			if err != nil {
				return err
			}
			cfg, err := config.Load(filepath.Join(root, configName))
			if err != nil {
				return tserr.ConfigErr("gc: load "+configName, err)
			}
			be, loc, err := resolveBackend(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			// Preflight before any List/Delete so a down node never yields a
			// partial sweep (also gates --dry-run's List).
			if err := preflightNode(cmd.Context(), loc); err != nil {
				return err
			}

			// Keep-set = union of every local branch's committed lock; preserve-set
			// protects preserve-marked shas regardless of keep membership.
			branchLocks, err := branchLocks(root)
			if err != nil {
				return err
			}
			keep := gc.BuildKeepSet(branchLocks)
			preserve := gc.BuildPreserveSet(branchLocks)

			stored, err := be.List(cmd.Context(), "objects/")
			if err != nil {
				return err
			}
			plan := gc.PlanSweep(stored, keep, preserve)

			out := cmd.OutOrStdout()
			for _, sha := range plan.Eligible {
				size := int64(-1)
				if m, e := be.Stat(cmd.Context(), "objects/"+sha); e == nil {
					size = m.Size
				}
				fmt.Fprintf(out, "delete objects/%s (%d bytes)\n", sha, size)
			}
			fmt.Fprintf(out, "gc: %d eligible, %d kept, %d preserved\n",
				len(plan.Eligible), plan.Kept, plan.Preserved)

			if dryRun {
				fmt.Fprintln(out, "(dry-run: nothing deleted)")
				return nil
			}
			n, err := gc.Sweep(cmd.Context(), be, plan, false)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "gc: deleted %d blob(s)\n", n)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be removed; delete nothing")
	return cmd
}

// branchLocks reads each local branch tip's committed tailvault.lock and parses
// it, skipping branches that have no lock. It returns branch name -> parsed lock
// for the GC keep-set union.
func branchLocks(root string) (map[string]*lock.Lock, error) {
	branches, err := gitglue.LocalBranches(root)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*lock.Lock, len(branches))
	for _, br := range branches {
		data, found, err := gitglue.ReadFileAtRef(root, br, lockName)
		if err != nil {
			return nil, err
		}
		if !found {
			continue // a branch with no committed lock contributes nothing
		}
		l, err := lock.Parse(data)
		if err != nil {
			return nil, tserr.ConfigErr("gc: parse "+br+":"+lockName, err)
		}
		out[br] = l
	}
	return out, nil
}
