package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/verify"
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
			if !rep.OK() {
				// Any finding is an integrity failure (exit bucket 5).
				return &tserr.Error{
					Code: tserr.ObjMissing,
					Cause: fmt.Sprintf("integrity check failed: %d corrupt, %d missing",
						len(rep.Corrupt), len(rep.Missing)),
					Fix: "re-push from a clone that has the content, or investigate the node",
				}
			}
			return nil
		},
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
