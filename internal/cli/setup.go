package cli

import "github.com/spf13/cobra"

func newSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Interactively register a node and write tailvault.toml + hooks",
		RunE:  notImplemented,
	}
}
