package cli

import "github.com/spf13/cobra"

func newTrackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "track <glob>",
		Short: "Add include rule(s) to tailvault.toml",
		RunE:  notImplemented,
	}
}
