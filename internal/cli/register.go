package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/gitglue"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/setup"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
)

// registerLocal runs the default `tailvault setup` flow: create a LOCAL storage
// location on this machine. It prompts for a name (defaulting to the current git
// repo's folder when inside one, else "home") and a store path (defaulting to
// ~/.tailvault/stores/<name>), then persists the entry. No tailnet node, no SSH.
func registerLocal(cmd *cobra.Command, name string) error {
	pr := setup.NewStdinPrompter(cmd.InOrStdin(), cmd.OutOrStdout())

	if name == "" {
		def := "home"
		if root, err := gitglue.RepoRoot(""); err == nil && root != "" {
			def = filepath.Base(root) // decision: derive the name from the repo folder
		}
		n, err := pr.AskString("location name", def)
		if err != nil {
			return err
		}
		if n == "" {
			return fmt.Errorf("setup: location name is required")
		}
		name = n
	}

	loc, err := setup.BuildLocalLocation(pr, name)
	if err != nil {
		return err
	}

	reg, err := locations.Load()
	if err != nil {
		return err
	}
	if err := reg.Add(name, loc); err != nil {
		return err
	}
	if err := reg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "registered local location %q (base_path %s)\n", name, loc.BasePath)
	return nil
}

// statusForDiscovery is the seam for tailnet peer discovery in the interactive
// flow. It defaults to the real local session but is overridden in tests so the
// flow runs deterministically with no real tailscale daemon (per the
// no-real-Tailscale test rule).
var statusForDiscovery = func(ctx context.Context) (tailscale.Status, error) {
	return tailscale.New().Status(ctx)
}

// registerInteractive runs the interactive node-registration flow shared by
// `setup` and the interactive form of `location add`: it enumerates online
// peers from the local Tailscale session (unless node is given), prompts for the
// remaining fields, and persists the entry via the locations registry.
//
// Discovery failure is non-fatal — it prints one stderr line and falls back to
// manual entry. The flow never performs a Tailscale login or API call.
func registerInteractive(cmd *cobra.Command, name, node string) error {
	pr := setup.NewStdinPrompter(cmd.InOrStdin(), cmd.OutOrStdout())

	if name == "" {
		n, err := pr.AskString("location name", "")
		if err != nil {
			return err
		}
		if n == "" {
			return fmt.Errorf("setup: location name is required")
		}
		name = n
	}

	var peers []setup.Peer
	if node == "" {
		st, err := statusForDiscovery(cmd.Context())
		if p, ok := setup.OnlinePeers(st, err); ok {
			peers = p
		} else if _, found := tailscale.Locate(); !found {
			// The binary itself is missing — a failed resolution, not a down
			// daemon. Flag the real fix (PATH / install / register) instead of a
			// generic "unavailable", then fall back to manual entry.
			fmt.Fprintln(cmd.ErrOrStderr(), "tailscale CLI not found on PATH or in any known location — peer auto-detect is off.")
			fmt.Fprintln(cmd.ErrOrStderr(), "  fix: install Tailscale, or run `tailvault config` to locate and register it (or set TAILVAULT_TAILSCALE).")
			fmt.Fprintln(cmd.ErrOrStderr(), "Entering manual mode.")
		} else {
			// Binary resolves but the session isn't usable (daemon down / logged out).
			fmt.Fprintln(cmd.ErrOrStderr(), "Tailscale peer discovery unavailable (daemon down or not logged in); entering manual mode.")
		}
	}

	loc, err := setup.BuildLocation(pr, peers, node)
	if err != nil {
		return err
	}

	reg, err := locations.Load()
	if err != nil {
		return err
	}
	if err := reg.Add(name, loc); err != nil {
		return err
	}
	if err := reg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "registered location %q (%s on %s)\n", name, loc.Backend, loc.Node)
	return nil
}
