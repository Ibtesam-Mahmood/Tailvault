package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/ingest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/setup"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// newVaultRestoreIdentityCmd implements manual identity recovery (D24): re-seed a
// rebuilt catalog entry with its ORIGINAL self-certifying id from a surviving
// genesis record (a pull receipt, a raw record, or a committed tailvault.lock v2
// entry via --lock/--path). Never implicit — always explicit, confirmed, WAL-audited, and
// PASSWORD-GATED: restore overwrites the genesis identity (the integrity root),
// so per the §16 amendment (DEV-48.2) it is a gated mutation (gateLocation gates
// SSH node-side only; a taildrive/local mount rides ACL + mount perms).
func newVaultRestoreIdentityCmd() *cobra.Command {
	var receipt, record, lockFile, lockPath, passwordFile string
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore-identity <location>/<current-path>",
		Short: "Re-seed a rebuilt catalog entry with its original self-certifying id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultRestoreIdentity(cmd, args[0], restoreSources{receipt, record, lockFile, lockPath}, yes, passwordFile)
		},
	}
	f := cmd.Flags()
	f.StringVar(&receipt, "receipt", "", "genesis source: a pull-receipt TOML file")
	f.StringVar(&record, "record", "", "genesis source: a raw genesis-record TOML file")
	f.StringVar(&lockFile, "lock", "", "genesis source: a tailvault.lock (v2) file (with --path)")
	f.StringVar(&lockPath, "path", "", "repo path of the lock entry (with --lock)")
	f.BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	f.StringVar(&passwordFile, "password-file", "", "read the vault password from this file (remote/SSH locations)")
	return cmd
}

type restoreSources struct {
	receipt, record, lockFile, lockPath string
}

func runVaultRestoreIdentity(cmd *cobra.Command, target string, src restoreSources, yes bool, passwordFile string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	locName, rel, err := splitLocationPath(target)
	if err != nil {
		return err
	}

	g, claimedID, err := loadGenesisSource(src)
	if err != nil {
		return err
	}

	// Self-certification gate: the record must hash to its own id (and to any id
	// the source carried).
	id, err := identity.VerifyID(g)
	if err != nil {
		return tserr.ConfigErr("restore-identity: invalid genesis record", err)
	}
	if claimedID != "" && !strings.EqualFold(claimedID, id) {
		return tserr.ConfigErr(fmt.Sprintf("restore-identity: record does not self-certify (source id %s, computed %s)", claimedID, id), nil)
	}

	be, loc, err := locationBackend(locName)
	if err != nil {
		return err
	}
	cat, err := readCatalog(ctx, be)
	if err != nil {
		return tserr.ConfigErr("restore-identity: read catalog", err)
	}
	if cat == nil {
		return tserr.ConfigErr("restore-identity: "+locName+" has no catalog (not bootstrapped)", nil)
	}
	f, ok := cat.Find(rel)
	if !ok {
		return tserr.ConfigErr(fmt.Sprintf("restore-identity: no catalog entry at %q", rel), nil)
	}
	if f.ID == id {
		fmt.Fprintf(out, "nothing to do: %s already carries id %s\n", rel, identity.Short(id))
		return nil
	}

	fmt.Fprintf(out, "restore %s: %s → %s (original)\n", rel, identity.Short(f.ID), identity.Short(id))
	if g.ContentSHA256 != f.SHA256 && g.ContentSHA256 != f.Genesis.ContentSHA256 {
		fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: record's original content sha (%s) matches neither the current nor genesis sha of %s — re-identifying the wrong file?\n",
			identity.Short(g.ContentSHA256), rel)
	}

	if !yes {
		pr := setup.NewStdinPrompter(cmd.InOrStdin(), out)
		ans, _ := pr.AskString("proceed with identity restoration? [y/N]", "n")
		if !isYes(ans) {
			fmt.Fprintln(out, "aborted")
			return nil
		}
	}

	// Mutating op that overwrites the integrity root → password-gated (DEV-48.2).
	// gateLocation enforces SSH node-side; taildrive/local is a no-op (DEV-46.8).
	if err := gateLocation(ctx, loc, be, locName, passwordFile); err != nil {
		return err
	}

	// Federation-wide collision guard (task-48): an id must never become live in
	// two places. The engine's local FindID check is defense-in-depth; the
	// authoritative guard fans out over the roster (task-32) here, in the command,
	// right before the mutation. A standalone (un-federated) location has no other
	// members to collide with, so the local guard alone suffices there.
	if err := assertNoFederationCollision(ctx, id); err != nil {
		return err
	}

	restored, err := ingest.RestoreIdentity(ctx, ingest.RestoreOpts{
		Backend: be, Log: &wal.Log{B: be}, Cat: cat, Node: loc.Node, Actor: initActor(cmd),
	}, rel, id, g)
	if err != nil {
		// A local same-catalog duplicate is the same integrity violation as a
		// cross-member one → the federation collision code (exit 6), not a config error.
		if errors.Is(err, ingest.ErrIDCollision) {
			return tserr.FedIDCollisionErr(identity.Short(id), loc.Node, err)
		}
		return tserr.ConfigErr("restore-identity: "+err.Error(), err)
	}
	fmt.Fprintf(out, "restored: %s now carries id %s\n", restored.Path, identity.Short(restored.ID))
	return nil
}

// assertNoFederationCollision hard-fails if id is already live anywhere in the
// federation — restoring it would create two live claims to one identity
// (task-48). It fans out over the roster via the resolution engine (task-32):
//   - Found (at home or elsewhere) → collision → TV-FED-04 (exit 6).
//   - PartialView → a member is unreachable, so absence (no collision) cannot be
//     proven → TV-FED-01 (exit 6); the user must bring members online and retry.
//   - Missing → no member holds the id → safe to proceed.
//
// A location with no federation roster (standalone vault) has no other members to
// collide with; the engine's local FindID guard is then the only and sufficient
// check, so a "no roster" discovery error is treated as "no collision".
func assertNoFederationCollision(ctx context.Context, id string) error {
	reg, err := locations.Load()
	if err != nil {
		return tserr.ConfigErr("restore-identity: load locations.toml", err)
	}
	roster, err := loadRoster(ctx, reg)
	if err != nil {
		return nil // un-federated location: no cross-member collision surface.
	}
	resolver := &fed.Resolver{
		Roster: roster,
		Q:      fed.NewBackendQuerier(backendForRegistry(reg)),
		Probe:  memberProbe(reg),
	}
	res, err := resolver.Resolve(ctx, id, "")
	if err != nil {
		if errors.Is(err, wal.ErrChainBroken) {
			return tserr.FedChainBrokenErr("", err) // exit 6
		}
		return err
	}
	switch res.Outcome {
	case fed.FoundAtHome, fed.FoundElsewhere:
		return tserr.FedIDCollisionErr(identity.Short(id), res.View.Member, nil)
	case fed.PartialView:
		return tserr.FedPartialViewErr(identity.Short(id), res.Reach.Unreachable, nil)
	case fed.Missing:
		return nil
	default:
		return fmt.Errorf("restore-identity: unexpected resolution outcome %s for %s", res.Outcome, identity.Short(id))
	}
}

// loadGenesisSource reads the genesis record from EXACTLY one source flag.
func loadGenesisSource(src restoreSources) (identity.Genesis, string, error) {
	n := 0
	for _, s := range []string{src.receipt, src.record, src.lockFile} {
		if s != "" {
			n++
		}
	}
	if n != 1 {
		return identity.Genesis{}, "", tserr.ConfigErr("restore-identity: pass exactly one of --receipt / --record / --lock", nil)
	}
	switch {
	case src.receipt != "":
		r, err := identity.ReadReceiptFile(src.receipt)
		if err != nil {
			return identity.Genesis{}, "", tserr.ConfigErr("restore-identity: read --receipt", err)
		}
		return r.Genesis, r.ID, nil
	case src.record != "":
		g, err := identity.ReadRecordFile(src.record)
		if err != nil {
			return identity.Genesis{}, "", tserr.ConfigErr("restore-identity: read --record", err)
		}
		return g, "", nil
	default: // --lock (DG-48.1) — a tailvault.lock (v2) embeds id+genesis per entry,
		// so a surviving clone's lock is an off-node identity backup. Needs --path to
		// name which entry to read.
		if src.lockPath == "" {
			return identity.Genesis{}, "", tserr.ConfigErr("restore-identity: --lock requires --path <repo-path> to name the lock entry", nil)
		}
		lk, err := lock.Load(src.lockFile)
		if err != nil {
			return identity.Genesis{}, "", tserr.ConfigErr("restore-identity: read --lock", err)
		}
		// Self-certify the source lock before trusting any genesis it carries: a v1
		// lock or a torn id↔genesis pairing must never seed an identity restore.
		if err := lk.Validate(); err != nil {
			return identity.Genesis{}, "", tserr.ConfigErr("restore-identity: --lock fails self-certification", err)
		}
		for i := range lk.Entries {
			e := &lk.Entries[i]
			if e.Path != src.lockPath {
				continue
			}
			// DG-35.1: push does not yet populate id/genesis into lock entries, so a
			// legitimately-written lock entry may carry none. The user named THIS entry
			// explicitly, so there is nothing to skip to — hard-fail clearly (never a
			// silent success) and point at the working sources.
			if e.ID == "" || e.Genesis == nil {
				return identity.Genesis{}, "", tserr.ConfigErr(fmt.Sprintf(
					"restore-identity: --lock entry %q carries no embedded genesis (lock id/genesis population is deferred — DG-35.1); use --receipt or --record", src.lockPath), nil)
			}
			return *e.Genesis, e.ID, nil
		}
		return identity.Genesis{}, "", tserr.ConfigErr(fmt.Sprintf("restore-identity: --lock has no entry at path %q", src.lockPath), nil)
	}
}

// splitLocationPath splits "<location>/<rel>" into its parts (restore-identity
// takes a logical path target; coder-c's parseTarget covers the id-prefix forms
// used by ls/get).
func splitLocationPath(target string) (loc, rel string, err error) {
	parts := strings.SplitN(target, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", tserr.ConfigErr("expected <location>/<path>, got "+target, nil)
	}
	return parts[0], parts[1], nil
}
