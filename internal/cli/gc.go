package cli

import "github.com/spf13/cobra"

func newGCCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gc",
		Short: "Prune unreferenced blobs per retention policy",
		RunE:  notImplemented,
	}
}
