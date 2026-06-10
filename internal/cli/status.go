package cli

import "github.com/spf13/cobra"

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show local-only / pushed / drifted / orphaned files",
		RunE:  notImplemented,
	}
}
