package cli

import (
	"context"
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
)

func newLocationCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "location",
		Short: "Manage storage locations",
	}
	c.AddCommand(newLocationAddCmd(), newLocationLsCmd())
	return c
}

func newLocationAddCmd() *cobra.Command {
	var (
		node     string
		basePath string
		backend  string
		user     string
		share    string
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Register a tailnode storage target (writes locations.toml)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// No --base-path means the non-scriptable fields weren't supplied:
			// run the interactive flow (pick-list unless --node, then prompts).
			if basePath == "" {
				return registerInteractive(cmd, args[0], node)
			}
			// Fully flag-driven (scriptable) path.
			reg, err := locations.Load()
			if err != nil {
				return err
			}
			loc := locations.Location{
				Node:     node,
				BasePath: basePath,
				Backend:  locations.Backend(backend),
				User:     user,
				Share:    share,
			}
			if err := reg.Add(args[0], loc); err != nil {
				return err
			}
			if err := reg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added location %q (%s on %s)\n", args[0], loc.Backend, loc.Node)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&node, "node", "", "MagicDNS name or 100.x IP of the storage node")
	f.StringVar(&basePath, "base-path", "", "base path on the node that holds objects/ and refs/")
	f.StringVar(&backend, "backend", "ssh", "transfer backend: ssh|taildrive")
	f.StringVar(&user, "user", "", "ssh user (ssh backend)")
	f.StringVar(&share, "share", "", "taildrive share name (taildrive backend)")
	return cmd
}

func newLocationLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List registered locations + live reachability",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, err := locations.Load()
			if err != nil {
				return err // registry read error is a real failure
			}
			return printLocations(cmd.Context(), cmd, reg, tailscale.New().Ping)
		},
	}
}

// printLocations renders the reachability table. ping is injected so tests can
// supply a stub (no real tailnet). Unreachability is reported as data; ls never
// exits non-zero just because a node is down.
func printLocations(ctx context.Context, cmd *cobra.Command, reg locations.Registry, ping locations.PingFunc) error {
	names := make([]string, 0, len(reg.Locations))
	for name := range reg.Locations {
		names = append(names, name)
	}
	sort.Strings(names)

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tNODE\tBACKEND\tREACHABLE\tDETAIL")
	for _, name := range names {
		loc := reg.Locations[name]
		r := locations.Check(ctx, name, loc, ping)
		mark := "no"
		if r.Reachable {
			mark = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", name, loc.Node, loc.Backend, mark, r.Detail)
	}
	return tw.Flush()
}
