package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newSetupCmd() *cobra.Command {
	var node string
	var remote bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Set up a storage location — local by default, or a remote node with --remote",
		Long: "Create a storage location for tailvault. By default this registers a " +
			"LOCAL content-addressed store on this machine (no tailnet node required). " +
			"Use --remote to register a remote tailnet node over SSH (the peer pick-list).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if remote {
				if err := registerInteractive(cmd, "", node); err != nil {
					return err
				}
			} else {
				if err := registerLocal(cmd, ""); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "next: run `tailvault init` to write tailvault.toml and install hooks")
			return nil
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "MagicDNS name or 100.x IP (remote only; skips the pick-list)")
	cmd.Flags().BoolVar(&remote, "remote", false, "register a remote tailnet node instead of a local store")
	return cmd
}
