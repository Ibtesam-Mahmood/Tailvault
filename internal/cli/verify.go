package cli

import "github.com/spf13/cobra"

func newVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Re-hash stored blobs; report corruption/missing",
		RunE:  notImplemented,
	}
}
