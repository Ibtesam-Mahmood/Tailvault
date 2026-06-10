package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/version"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "tailvault",
		Short:         "Tailscale-native large-file store for git",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.AddCommand(
		newSetupCmd(), newInitCmd(), newLocationCmd(),
		newTrackCmd(), newStatusCmd(), newPushCmd(), newPullCmd(),
		newGCCmd(), newVerifyCmd(), newRevertCmd(),
		newMergeLockCmd(), newFilterCleanCmd(), newFilterSmudgeCmd(),
	)
	return root
}

// Execute runs the root command, returning any error for main to map into a
// bucketed process exit code via tserr.ExitCodeFor.
func Execute() error {
	return newRootCmd().Execute()
}

// notImplemented is the shared stub body for commands not yet wired up.
func notImplemented(cmd *cobra.Command, _ []string) error {
	fmt.Fprintln(cmd.OutOrStdout(), "not implemented yet")
	return nil
}
