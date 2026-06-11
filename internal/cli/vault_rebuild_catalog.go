package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/ingest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/setup"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// newVaultRebuildCatalogCmd implements `tailvault vault rebuild-catalog
// <location>` — the SG-6 disaster-recovery surface that reconstructs a missing or
// torn meta/catalog.toml as a pure PROJECTION of the node's WAL (the catalog has
// always been documented as a projection of the WAL — the recovery record). It
// replays every DONE op in seq order through ingest.ProjectCatalog and writes the
// result over the backend with backend.PutOverwrite (atomic replace).
//
// It is EXPLICIT and node-mutating — never automatic, never folded into pull or
// heal (which are repo-side only). It is PASSWORD-GATED like every other mutating
// vault op (gateLocation: SSH node-side; taildrive/local rides ACL + mount perms,
// DEV-46.8). It appends NO WAL op: the WAL is the source being replayed, not
// extended.
//
// A broken WAL hash chain is a hard fail (TV-FED-03, exit 6): a tampered/torn
// recovery record must never silently drive a rebuild. When the catalog is
// missing/torn AND no surviving federation member can be reached to re-source the
// roster, it refuses rather than silently writing a federation-less catalog
// (which would orphan a federated node) — pass --standalone to assert the node is
// genuinely un-federated.
func newVaultRebuildCatalogCmd() *cobra.Command {
	var vaultName, passwordFile string
	var dryRun, yes, standalone bool
	cmd := &cobra.Command{
		Use:   "rebuild-catalog <location>",
		Short: "Reconstruct a missing/torn catalog from the node's WAL (SG-6 recovery)",
		Long: `Rebuild meta/catalog.toml on a storage location as a pure projection of its
WAL — the disaster-recovery path when the catalog is lost or corrupt.

Every DONE WAL op is replayed in seq order to reconstruct the file list; the
header (node, vault_name, federation roster) is preserved from the existing
catalog when it is still readable, or re-sourced from the location record and
surviving federation members when it is not. A broken WAL hash chain hard-fails
(the recovery record must be trustworthy). The rebuilt catalog is written with an
atomic overwrite; no WAL op is appended.

This is an explicit, node-mutating, password-gated recovery command — it is never
run automatically. Use --dry-run to preview.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultRebuildCatalog(cmd, args[0], rebuildOpts{
				vaultName: vaultName, passwordFile: passwordFile,
				dryRun: dryRun, yes: yes, standalone: standalone,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&vaultName, "vault-name", "", "vault_name for the rebuilt header (overrides the recovered value)")
	f.BoolVar(&dryRun, "dry-run", false, "preview the rebuild without writing")
	f.BoolVar(&yes, "yes", false, "skip the overwrite confirmation prompt")
	f.BoolVar(&standalone, "standalone", false, "rebuild a federation-less catalog when no roster can be recovered")
	f.StringVar(&passwordFile, "password-file", "", "read the vault password from this file (remote/SSH locations)")
	return cmd
}

type rebuildOpts struct {
	vaultName, passwordFile string
	dryRun, yes, standalone bool
}

func runVaultRebuildCatalog(cmd *cobra.Command, name string, opts rebuildOpts) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	ew := cmd.ErrOrStderr()

	be, loc, err := locationBackend(name)
	if err != nil {
		return err
	}

	// The WAL IS the recovery record: read it chain-verified. A broken chain is a
	// federation-integrity failure (TV-FED-03, exit 6) — never rebuild from a
	// tampered/torn journal.
	recs, err := (&wal.Log{B: be}).Read(ctx)
	if err != nil {
		if errors.Is(err, wal.ErrChainBroken) {
			return tserr.FedChainBrokenErr(loc.Node, err)
		}
		return tserr.ConfigErr("rebuild-catalog: read WAL on "+name, err)
	}

	base, state, oldCount, err := rebuildBaseHeader(ctx, be, loc, name, opts)
	if err != nil {
		return err
	}

	rebuilt, err := ingest.ProjectCatalog(base, recs, loc.Node)
	if err != nil {
		return tserr.ConfigErr("rebuild-catalog: project WAL", err)
	}
	if err := rebuilt.Validate(); err != nil {
		return tserr.ConfigErr("rebuild-catalog: projected catalog is invalid", err)
	}

	fmt.Fprintf(out, "rebuild %s: catalog %s; replaying %d WAL op(s) → %d file(s)",
		name, state, len(recs), len(rebuilt.Files))
	if oldCount >= 0 {
		fmt.Fprintf(out, " (was %d)", oldCount)
	}
	fmt.Fprintln(out)
	if rebuilt.Federation.FedID != "" {
		fmt.Fprintf(out, "federation roster preserved: fed_id %s, %d member(s)\n", rebuilt.Federation.FedID, len(rebuilt.Federation.Members))
	} else {
		fmt.Fprintln(ew, "warning: rebuilt header has no [federation] section (standalone)")
	}

	if opts.dryRun {
		fmt.Fprintln(out, "dry-run: no changes written")
		return nil
	}

	if !opts.yes {
		pr := setup.NewStdinPrompter(cmd.InOrStdin(), out)
		ans, _ := pr.AskString(fmt.Sprintf("overwrite meta/catalog.toml on %s? [y/N]", name), "n")
		if !isYes(ans) {
			fmt.Fprintln(out, "aborted")
			return nil
		}
	}

	// Overwriting the catalog is a mutating op → password-gated (SSH node-side;
	// taildrive/local rides ACL + mount perms, DEV-46.8). Gate BEFORE the write.
	if err := gateLocation(ctx, loc, be, name, opts.passwordFile); err != nil {
		return err
	}

	b, err := catalog.Encode(rebuilt)
	if err != nil {
		return tserr.ConfigErr("rebuild-catalog: encode catalog", err)
	}
	// Atomic replace; NO WAL op (the WAL is the source, not a sink). PutOverwrite
	// is the SG-6 mutable-key primitive (temp+fsync+rename / cat>tmp&&mv).
	if err := be.PutOverwrite(ctx, catalogStoreKey, bytes.NewReader(b)); err != nil {
		return err
	}
	fmt.Fprintf(out, "rebuilt: %s now lists %d file(s)\n", name, len(rebuilt.Files))
	return nil
}

// rebuildBaseHeader sources the header for the rebuilt catalog. When the existing
// catalog is still readable its header (version/vault_name/node/federation) is
// kept verbatim — the common "file list corrupted, header intact" case. When the
// catalog is MISSING or TORN (unparseable) the header is reconstructed from the
// location record (node) and the federation roster re-discovered across surviving
// members; refusing to silently de-federate a node whose roster cannot be proven
// (unless --standalone). oldCount is the prior file count (-1 when unknown).
func rebuildBaseHeader(ctx context.Context, be backend.Backend, loc locations.Location, name string, opts rebuildOpts) (base *catalog.Catalog, state string, oldCount int, err error) {
	var buf bytes.Buffer
	getErr := be.Get(ctx, catalogStoreKey, &buf)
	switch {
	case getErr == nil:
		cat, perr := catalog.Parse(buf.Bytes())
		if perr == nil {
			cat.Files = nil // discard the (possibly bad) list; ProjectCatalog reprojects it
			if opts.vaultName != "" {
				cat.VaultName = opts.vaultName
			}
			oldCat, _ := catalog.Parse(buf.Bytes())
			return cat, "present", len(oldCat.Files), nil
		}
		// Present but unparseable → torn; fall through to reconstruction.
		state = "torn (unparseable)"
	case errors.Is(getErr, backend.ErrNotExist):
		state = "missing"
	default:
		return nil, "", -1, tserr.ConfigErr("rebuild-catalog: read existing catalog on "+name, getErr)
	}

	// Reconstruct the header. Node is authoritative from the location record.
	hdr := &catalog.Catalog{Version: catalog.SchemaVersion, VaultName: opts.vaultName, Node: loc.Node}

	// Re-source the federation roster from surviving members (it is replicated, so
	// any one member's catalog yields it). loadRoster errors only when NO member
	// answers with a roster.
	reg, rerr := locations.Load()
	if rerr != nil {
		return nil, "", -1, tserr.ConfigErr("rebuild-catalog: load locations.toml", rerr)
	}
	roster, lrerr := loadRoster(ctx, reg)
	switch {
	case lrerr == nil:
		hdr.Federation = catalog.Federation{FedID: roster.FedID, Members: roster.Members}
	case opts.standalone:
		// Explicitly acknowledged un-federated: leave the [federation] section empty.
	default:
		// We cannot prove this node's federation membership and the user has not
		// asserted it is standalone — refuse rather than silently de-federate.
		return nil, "", -1, tserr.ConfigErr(
			"rebuild-catalog: catalog is "+state+" and no federation roster could be recovered from any reachable member; "+
				"bring a federation member online and retry, or pass --standalone to rebuild a federation-less catalog", nil)
	}
	return hdr, state, -1, nil
}
