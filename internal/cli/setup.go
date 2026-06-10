package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newSetupCmd() *cobra.Command {
	var node string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Interactively register a storage node (then run `tailvault init`)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := registerInteractive(cmd, "", node); err != nil {
				return err
			}
			// TODO(task-18): delegate to the `init` path here to write
			// tailvault.toml and install git hooks once init is merged.
			fmt.Fprintln(cmd.OutOrStdout(), "next: run `tailvault init` to write tailvault.toml and install hooks")
			return nil
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "MagicDNS name or 100.x IP (skips the pick-list)")
	return cmd
}
