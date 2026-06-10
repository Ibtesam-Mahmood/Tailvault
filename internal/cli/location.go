package cli

import "github.com/spf13/cobra"

func newLocationCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "location",
		Short: "Manage storage locations",
	}
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Register a tailnode storage target (writes locations.toml)",
		Args:  cobra.ExactArgs(1),
		RunE:  notImplemented,
	}
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List registered locations + live reachability",
		RunE:  notImplemented,
	}
	c.AddCommand(add, ls)
	return c
}
