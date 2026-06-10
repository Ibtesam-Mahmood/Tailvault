package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/push"
)

func newPushCmd() *cobra.Command {
	var (
		branch string
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Upload diffs, GC deletes, update lock",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := findRepoRoot()
			if err != nil {
				return err
			}
			cfg, err := config.Load(filepath.Join(root, configName))
			if err != nil {
				return err
			}
			lk, err := loadLockOrEmpty(filepath.Join(root, lockName))
			if err != nil {
				return err
			}
			be, loc, err := resolveBackend(cmd.Context(), cfg)
			if err != nil {
				return err
			}

			deps := push.Deps{
				Backend:     be,
				Preflight:   func(ctx context.Context) error { return preflightNode(ctx, loc) },
				Whois:       whoisSelf,
				GitIdentity: gitEmail,
			}
			res, err := push.Run(cmd.Context(), root, cfg, lk, deps, push.Options{Branch: branch, DryRun: dryRun})
			if err != nil {
				return err
			}
			verb := "pushed"
			if dryRun {
				verb = "(dry-run)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s uploaded %d, deduped %d, renamed %d, dropped %d, marked-gc %d\n",
				verb, len(res.Uploaded), len(res.Deduped), len(res.Renamed), len(res.Dropped), len(res.MarkedGC))
			return nil
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "", "branch name recorded as lock provenance")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report the plan without uploading blobs or writing the lock")
	return cmd
}
