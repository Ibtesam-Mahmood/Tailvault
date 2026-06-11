package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/pull"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

func newPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Fetch blobs the current tree/branch needs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := findRepoRoot()
			if err != nil {
				return err
			}
			cfg, err := loadConfig(root)
			if err != nil {
				return err
			}
			lk, err := loadLockOrEmpty(filepath.Join(root, lockName))
			if err != nil {
				return err
			}
			be, loc, err := resolveBackend(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			deps := pull.Deps{
				Backend:      be,
				Preflight:    func(ctx context.Context) error { return preflightNode(ctx, loc) },
				ResolveEntry: federatedResolveEntry(cmd.Context()),
			}
			res, err := pull.Run(cmd.Context(), root, lk, deps)
			if err != nil {
				return err
			}
			for _, w := range res.Warnings {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+w)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "fetched %d, skipped %d\n", len(res.Fetched), len(res.Skipped))
			return nil
		},
	}
}

// federatedResolveEntry builds pull's federation seam (D5). It returns nil when
// the repo is NOT federated (no roster discoverable) so pull keeps its exact v1
// single-backend behavior. Otherwise it returns a closure that resolves a
// federated lock entry through the resolution engine: a moved/foreign home is
// fetched from the answering member with a WARN; a home that left/evicted WARNs
// even when found (D28); a PartialView hard-fails (exit 6) and a genuinely
// missing blob hard-fails (exit 5) — hard-fail reserved for unprovable states.
func federatedResolveEntry(ctx context.Context) func(context.Context, lock.Entry) (backend.Backend, string, error) {
	reg, err := locations.Load()
	if err != nil {
		return nil
	}
	roster, err := loadRoster(ctx, reg)
	if err != nil {
		return nil // non-federated repo — v1 path
	}
	resolver := buildResolver(reg, roster)
	bf := backendForRegistry(reg)

	return func(ctx context.Context, e lock.Entry) (backend.Backend, string, error) {
		res, rerr := resolver.Resolve(ctx, e.ID, e.Location)
		if rerr != nil {
			if errors.Is(rerr, wal.ErrChainBroken) {
				return nil, "", tserr.FedChainBrokenErr(e.Location, rerr) // exit 6
			}
			return nil, "", rerr
		}
		warn, oerr := resolveOutcome(res, e.ID)
		if oerr != nil {
			return nil, "", oerr // PartialView (exit 6) / Missing (exit 5)
		}

		// The blob lives at the resolver's winning member (single-home, D3).
		member := e.Location
		if res.View.Found {
			member = res.View.Member
		}
		m, ok := roster.Find(member)
		if !ok {
			return nil, "", tserr.ConfigErr("pull: resolved member "+member+" is not in the federation roster", nil)
		}
		be, berr := bf(m)
		if berr != nil {
			return nil, "", berr
		}

		warnMsg := ""
		if warn { // FoundElsewhere — name the new home + short id
			warnMsg = fmt.Sprintf("%s (id %s) moved to %s — run `tailvault heal`", e.Path, identity.Short(e.ID), member)
		}
		// D28: the recorded home left/evicted the federation — WARN even when the
		// blob is still found (at home or elsewhere); repush/heal is needed.
		if home, ok := roster.Find(e.Location); ok && home.Status != catalog.StatusActive {
			warnMsg = fmt.Sprintf("%s (id %s): home %q %s the federation — repush or heal", e.Path, identity.Short(e.ID), e.Location, statusVerb(home.Status))
		}
		return be, warnMsg, nil
	}
}

// statusVerb renders a non-active member status as a past-tense clause for WARN
// lines (D28).
func statusVerb(status string) string {
	switch status {
	case catalog.StatusEvicted:
		return "was evicted from"
	case catalog.StatusLeft:
		return "left"
	default:
		return "is no longer active in"
	}
}
