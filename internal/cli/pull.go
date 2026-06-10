package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/pull"
)

func newPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Fetch blobs the current tree/branch needs",
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
			deps := pull.Deps{
				Backend:   be,
				Preflight: func(ctx context.Context) error { return preflightNode(ctx, loc) },
			}
			res, err := pull.Run(cmd.Context(), root, lk, deps)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "fetched %d, skipped %d\n", len(res.Fetched), len(res.Skipped))
			return nil
		},
	}
}
