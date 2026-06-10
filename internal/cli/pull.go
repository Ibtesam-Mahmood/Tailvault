package cli

import "github.com/spf13/cobra"

func newPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Fetch blobs the current tree/branch needs",
		RunE:  notImplemented,
	}
}
