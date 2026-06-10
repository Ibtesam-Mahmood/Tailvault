package cli

import "github.com/spf13/cobra"

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Write tailvault.toml + .gitattributes and install hooks",
		RunE:  notImplemented,
	}
}
