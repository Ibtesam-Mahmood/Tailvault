package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/verify"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

func newVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Re-hash stored blobs; report corruption and missing objects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := findRepoRoot()
			if err != nil {
				return err
			}
			cfg, err := config.Load(filepath.Join(root, configName))
			if err != nil {
				return tserr.ConfigErr("verify: load "+configName, err)
			}
			be, loc, err := resolveBackend(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			if err := preflightNode(cmd.Context(), loc); err != nil {
				return err
			}
			lk, err := loadLockOrEmpty(filepath.Join(root, lockName))
			if err != nil {
				return tserr.ConfigErr("verify: load "+lockName, err)
			}

			rep, err := verify.Run(cmd.Context(), be, lk)
			if err != nil {
				return err
			}
			printVerify(cmd, rep)

			// 3-way verify (task-38): reconcile lock ↔ catalog ↔ disk + spot-check the
			// WAL. Runs even when the catalog is absent so a missing/torn catalog on a
			// vault that HAS committed WAL history is flagged (review-38 LOW-2) rather
			// than silently skipped; ThreeWay returns no 3-way findings for a genuinely
			// non-federated vault (no catalog, no WAL history).
			ctx := cmd.Context()
			cat, err := readCatalog(ctx, be)
			if err != nil {
				return tserr.ConfigErr("verify: read catalog", err)
			}
			root3, skipDisk := loc.BasePath, true
			if loc.Backend == locations.BackendTaildrive {
				skipDisk = false // local/mounted root → can check manual-file disk
			}
			findings, err := verify.ThreeWay(ctx, root3, lk, cat, &wal.Log{B: be},
				verify.Options{SkipDisk: skipDisk})
			if err != nil {
				return err
			}
			printThreeWay(cmd, findings)

			// Combine severity: v1 corrupt/missing (exit 5) + 3-way (5 or 6).
			code := verify.ExitCode(findings)
			if !rep.OK() && code < 5 {
				code = 5
			}
			switch {
			case code >= 6:
				return tserr.FedChainBrokenErr(loc.Node, fmt.Errorf("WAL chain verification failed"))
			case code >= 5:
				return &tserr.Error{
					Code:  tserr.ObjMissing,
					Cause: "integrity check failed (see findings above)",
					Fix:   "re-push from a clone, run `tailvault heal`/`vault scan`/`vault rebuild-catalog`/`ops` per the finding, or investigate the node",
				}
			}
			return nil
		},
	}
}

// printThreeWay prints the 3-way findings grouped with their repair pointers.
func printThreeWay(cmd *cobra.Command, fs []verify.ThreeFinding) {
	out := cmd.OutOrStdout()
	for _, f := range fs {
		if f.Path != "" {
			fmt.Fprintf(out, "%-16s %s (%s): %s\n", f.Kind, f.Path, f.ID, f.Detail)
		} else {
			fmt.Fprintf(out, "%-16s %s\n", f.Kind, f.Detail)
		}
	}
	if len(fs) > 0 {
		fmt.Fprintf(out, "verify(3-way): %d finding(s)\n", len(fs))
	}
}

func printVerify(cmd *cobra.Command, rep verify.Report) {
	out := cmd.OutOrStdout()
	for _, f := range rep.Corrupt {
		fmt.Fprintf(out, "CORRUPT  objects/%s (bytes hash to %s)\n", f.Key, f.Got)
	}
	for _, f := range rep.Missing {
		fmt.Fprintf(out, "MISSING  objects/%s (referenced by %v)\n", f.Key, f.Paths)
	}
	fmt.Fprintf(out, "verify: checked %d, corrupt %d, missing %d\n",
		rep.Checked, len(rep.Corrupt), len(rep.Missing))
}
