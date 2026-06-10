package cli

import "github.com/spf13/cobra"

func newPushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push",
		Short: "Upload diffs, GC deletes, update lock",
		RunE:  notImplemented,
	}
}
