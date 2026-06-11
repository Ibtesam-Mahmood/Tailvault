package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/setup"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
)

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
		} else {
			fmt.Fprintln(cmd.ErrOrStderr(), "Tailscale peer discovery unavailable; entering manual mode.")
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
