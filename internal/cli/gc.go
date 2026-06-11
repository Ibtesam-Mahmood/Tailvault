package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/gc"
	"github.com/Ibtesam-Mahmood/tailvault/internal/gitglue"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

func newGCCmd() *cobra.Command {
	var dryRun bool
	var passwordFile string
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Prune unreferenced blobs per retention policy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := findRepoRoot()
			if err != nil {
				return err
			}
			cfg, err := config.Load(filepath.Join(root, configName))
			if err != nil {
				return tserr.ConfigErr("gc: load "+configName, err)
			}
			be, loc, err := resolveBackend(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			// Preflight before any List/Delete so a down node never yields a
			// partial sweep (also gates --dry-run's List).
			if err := preflightNode(cmd.Context(), loc); err != nil {
				return err
			}

			// Keep-set = union of every local branch's committed lock; preserve-set
			// protects preserve-marked shas regardless of keep membership.
			branchLocks, err := branchLocks(root)
			if err != nil {
				return err
			}
			keep := gc.BuildKeepSet(branchLocks)
			preserve := gc.BuildPreserveSet(branchLocks)

			ctx := cmd.Context()
			stored, err := be.List(ctx, "objects/")
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			// Federation detection: a vault is federated when its catalog carries a
			// fed_id. A federated gc engages the three federation gates (D13/D14/D27);
			// a non-federated vault keeps the exact v1 behavior.
			cat, err := readCatalogMaybe(ctx, be)
			if err != nil {
				return tserr.ConfigErr("gc: read catalog", err)
			}
			if cat != nil && cat.Federation.FedID != "" {
				return runFederatedGC(ctx, out, be, loc, cfg.Storage.Location, passwordFile, cat, stored, keep, preserve, dryRun)
			}

			// Non-federated (v1) path — unchanged.
			plan := gc.PlanSweep(stored, keep, preserve)
			for _, sha := range plan.Eligible {
				size := int64(-1)
				if m, e := be.Stat(ctx, "objects/"+sha); e == nil {
					size = m.Size
				}
				fmt.Fprintf(out, "delete objects/%s (%d bytes)\n", sha, size)
			}
			fmt.Fprintf(out, "gc: %d eligible, %d kept, %d preserved\n",
				len(plan.Eligible), plan.Kept, plan.Preserved)

			if dryRun {
				fmt.Fprintln(out, "(dry-run: nothing deleted)")
				return nil
			}
			// gc DELETES blobs on the node → password-gated (§16 D9), mirroring
			// rm/mv: gate BEFORE any Delete. SSH verifies node-side; taildrive/local
			// is a no-op (DEV-46.8). A v1 vault on an SSH node is a remote delete and
			// is gated just like the federated path.
			if err := gateLocation(ctx, loc, be, cfg.Storage.Location, passwordFile); err != nil {
				return err
			}
			n, err := gc.Sweep(ctx, be, plan, false)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "gc: deleted %d blob(s)\n", n)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be removed; delete nothing")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "read the vault password from this file (remote/SSH gc)")
	return markGated(cmd)
}

// branchLocks reads each local branch tip's committed tailvault.lock and parses
// it, skipping branches that have no lock. It returns branch name -> parsed lock
// for the GC keep-set union.
func branchLocks(root string) (map[string]*lock.Lock, error) {
	branches, err := gitglue.LocalBranches(root)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*lock.Lock, len(branches))
	for _, br := range branches {
		data, found, err := gitglue.ReadFileAtRef(root, br, lockName)
		if err != nil {
			return nil, err
		}
		if !found {
			continue // a branch with no committed lock contributes nothing
		}
		l, err := lock.Parse(data)
		if err != nil {
			return nil, tserr.ConfigErr("gc: parse "+br+":"+lockName, err)
		}
		out[br] = l
	}
	return out, nil
}

// readCatalogMaybe reads the home node's catalog over the backend, returning
// (nil, nil) when there is none — a non-federated v1 vault.
func readCatalogMaybe(ctx context.Context, be backend.Backend) (*catalog.Catalog, error) {
	var buf bytes.Buffer
	if err := be.Get(ctx, "meta/catalog.toml", &buf); err != nil {
		if errors.Is(err, backend.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return catalog.Parse(buf.Bytes())
}

// runFederatedGC engages the three federation gates and journals the sweep,
// mapping the engine's plain errors to tserr at the boundary (§8): the
// all-members gate → TV-FED-02 (exit 6), a broken WAL chain → TV-FED-03 (exit 6).
func runFederatedGC(ctx context.Context, out io.Writer, be backend.Backend, loc locations.Location, locName, passwordFile string,
	cat *catalog.Catalog, stored []string, keep, preserve gc.KeepSet, dryRun bool) error {
	roster, err := fed.FromCatalog(cat)
	if err != nil {
		return tserr.ConfigErr("gc: read federation roster", err)
	}
	ts := tailscale.New()
	probe := func(ctx context.Context, m catalog.Member) error { return ts.Ping(ctx, m.Node) }
	actor, err := whoisSelf(ctx)
	if err != nil || actor == "" {
		actor = gitEmail()
	}
	fctx := &gc.FedContext{
		Backend:        be,
		Roster:         roster,
		Probe:          probe,
		Cat:            cat,
		Log:            &wal.Log{B: be},
		Actor:          actor,
		PersistCatalog: persistCatalogOverBackend(be),
	}

	p, err := gc.PlanFederated(ctx, fctx, stored, keep, preserve)
	if err != nil {
		return mapFedGCErr(cat.Node, err)
	}

	for _, sha := range p.Eligible {
		size := int64(-1)
		if m, e := be.Stat(ctx, "objects/"+sha); e == nil {
			size = m.Size
		}
		fmt.Fprintf(out, "delete objects/%s (%d bytes)\n", sha, size)
	}
	fmt.Fprintf(out, "gc: %d eligible, %d kept, %d preserved, %d skipped (in-flight ops)\n",
		len(p.Eligible), p.Kept, p.Preserved, len(p.SkippedPending))
	for _, sha := range p.SkippedPending {
		fmt.Fprintf(out, "skip objects/%s (pending WAL op) — re-run gc after `tailvault ops` clears them\n", sha)
	}

	if dryRun {
		fmt.Fprintln(out, "(dry-run: nothing deleted)")
		return nil
	}
	// Password gate BEFORE the OpGC intent / any Delete (§16 D9). SSH verifies
	// node-side; taildrive/local is a no-op (DEV-46.8).
	if err := gateLocation(ctx, loc, be, locName, passwordFile); err != nil {
		return err
	}
	rep, err := gc.SweepFederated(ctx, fctx, p, false)
	if err != nil {
		return mapFedGCErr(cat.Node, err)
	}
	fmt.Fprintf(out, "gc: deleted %d blob(s), %d re-skipped (raced ops)\n", rep.Deleted, len(rep.SkippedPending))
	return nil
}

// mapFedGCErr maps the gc engine's plain errors to tserr codes at the boundary.
func mapFedGCErr(node string, err error) error {
	var nam *gc.NeedAllMembersError
	if errors.As(err, &nam) {
		return tserr.FedNeedAllMembersErr("gc", nam.Unreachable, err)
	}
	if errors.Is(err, wal.ErrChainBroken) {
		return tserr.FedChainBrokenErr(node, err)
	}
	return err
}

// persistCatalogOverBackend atomically overwrites the single-key catalog after a
// sweep via backend.PutOverwrite (temp+fsync+rename / SSH mv) — atomic on every
// backend, so a crash leaves the old or the new catalog, never neither (SG-6 fix;
// replaces the earlier non-atomic Delete-then-Put). The gc WAL op additionally
// stays "intent" on failure so `tailvault ops` can recover.
func persistCatalogOverBackend(be backend.Backend) func(context.Context, *catalog.Catalog) error {
	return func(ctx context.Context, c *catalog.Catalog) error {
		bs, err := catalog.Encode(c)
		if err != nil {
			return err
		}
		return be.PutOverwrite(ctx, "meta/catalog.toml", bytes.NewReader(bs))
	}
}
