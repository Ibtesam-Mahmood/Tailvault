package cli

import "github.com/spf13/cobra"

func newRevertCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revert <path> <sha>",
		Short: "Repoint a history-on file to an older blob",
		Args:  cobra.ExactArgs(2),
		RunE:  notImplemented,
	}
}
