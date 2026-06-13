package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/setup"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

func newLocationCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "location",
		Short: "Manage storage locations",
	}
	c.AddCommand(newLocationAddCmd(), newLocationLsCmd(), newLocationRmCmd())
	return c
}

func newLocationRmCmd() *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Un-register a storage location (double-confirmed; --purge also deletes the local store data)",
		Long: "Remove a location from locations.toml. This is always double-confirmed. " +
			"Un-registering does NOT touch stored bytes. With --purge (local backend " +
			"only) it ALSO deletes the store's data dirs (objects/, refs/, meta/) under " +
			"base_path — guarded by an additional, third confirmation. --purge never " +
			"deletes anything other than tailvault's own store dirs, so a store rooted " +
			"in a folder with other files leaves those files intact.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLocationRm(cmd, args[0], purge)
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also delete the on-disk store data (local backend only; adds a 3rd confirmation)")
	return cmd
}

func runLocationRm(cmd *cobra.Command, name string, purge bool) error {
	reg, err := locations.Load()
	if err != nil {
		return err
	}
	loc, ok := reg.Locations[name]
	if !ok {
		return tserr.ConfigErr(fmt.Sprintf("location %q is not registered", name), nil)
	}
	if purge && loc.Backend != locations.BackendLocal {
		return tserr.ConfigErr(fmt.Sprintf("--purge only supports the local backend; %q is %s — remove it without --purge", name, loc.Backend), nil)
	}

	pr := setup.NewStdinPrompter(cmd.InOrStdin(), cmd.OutOrStdout())
	out := cmd.OutOrStdout()

	// Always double-confirm (both default to "no").
	if !askYesNo(pr, fmt.Sprintf("Remove location %q (%s) from the registry?", name, loc.Backend), false) {
		fmt.Fprintln(out, "aborted")
		return nil
	}
	if !askYesNo(pr, fmt.Sprintf("Confirm: un-register %q?", name), false) {
		fmt.Fprintln(out, "aborted")
		return nil
	}
	// --purge: one MORE confirmation before any bytes are erased.
	if purge {
		if !askYesNo(pr, fmt.Sprintf("ALSO DELETE the store data (objects/, refs/, meta/) under %s? This permanently erases stored blobs.", loc.BasePath), false) {
			fmt.Fprintln(out, "aborted")
			return nil
		}
	}

	if err := reg.Remove(name); err != nil {
		return err
	}
	if err := reg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed location %q from the registry\n", name)

	if purge {
		if err := purgeLocalStore(loc.BasePath); err != nil {
			return tserr.ConfigErr(fmt.Sprintf("purge: delete store data under %s", loc.BasePath), err)
		}
		fmt.Fprintf(out, "purged store data under %s\n", loc.BasePath)
	}
	return nil
}

// purgeLocalStore deletes only tailvault's own store directories under base — it
// never RemoveAll's base itself (which could nuke unrelated files when the store
// is rooted in a shared folder). After clearing them it removes base only if it
// is now empty (best-effort).
func purgeLocalStore(base string) error {
	for _, sub := range []string{"objects", "refs", "meta"} {
		if err := os.RemoveAll(filepath.Join(base, sub)); err != nil {
			return err
		}
	}
	_ = os.Remove(base) // succeeds only if empty; ignore otherwise
	return nil
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
			// A local store must not live inside a git repo (blobs would pollute it).
			if loc.Backend == locations.BackendLocal {
				if err := guardLocalStorePath(args[0], loc.BasePath); err != nil {
					return err
				}
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
