package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/setup"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// newVaultRmCmd implements `tailvault vault rm <logical-path | id>`: the ONLY way
// a sync_mode=manual file dies (D14) — gc never considers manual files and scan
// never deletes them, so deliberate removal is a first-class command. It resolves
// the file's LIVE home (following moved_to, so deleting a moved file deletes the
// real bytes, never the forwarder stub), runs the full WAL lifecycle (intent →
// delete blob → remove catalog entry → done), and refuses to act under ambiguity.
//
// A delete mutates a node, so it is PASSWORD-GATED (D9 / §16). It also requires
// confirmation (interactive y/N, or --yes for scripts): a non-TTY run without
// --yes is refused before any work. Removing a git-mode file is allowed but gets
// a loud extra warning — repos referencing the bytes hard-fail on next pull.
func newVaultRmCmd() *cobra.Command {
	var yes, jsonOut bool
	var passwordFile string
	cmd := &cobra.Command{
		Use:   "rm <logical-path | id>",
		Short: "Delete a file from its storage location (the only way a manual file dies)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultRm(cmd, args[0], rmFlags{yes: yes, passwordFile: passwordFile, json: jsonOut})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the interactive confirmation (for scripts)")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "read the vault password from this file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return markGated(cmd)
}

type rmFlags struct {
	yes          bool
	passwordFile string
	json         bool
}

// rmJSON is the scriptable --json contract. It carries the genesis record (the
// last trace of the deleted identity) for tooling — the plain text output omits
// it (filename privacy, Block 5).
type rmJSON struct {
	ID          string          `json:"id"`
	Short       string          `json:"short_id"`
	Home        string          `json:"home"`
	Path        string          `json:"path"`
	SHA256      string          `json:"sha256"`
	SyncMode    string          `json:"sync_mode"`
	BlobDeleted bool            `json:"blob_deleted"` // false when another entry still references the sha
	Genesis     catalog.Genesis `json:"genesis"`
}

func runVaultRm(cmd *cobra.Command, arg string, fl rmFlags) error {
	ctx := cmd.Context()
	reg, err := locations.Load()
	if err != nil {
		return tserr.ConfigErr("load locations.toml", err)
	}
	roster, err := loadRoster(ctx, reg)
	if err != nil {
		return err
	}

	// Resolve to the LIVE home (follows moved_to / fan-out; PartialView→TV-FED,
	// Missing→TV-OBJ). Deletes never tolerate ambiguity.
	file, home, err := resolveSource(ctx, reg, roster, arg)
	if err != nil {
		return err
	}
	b, loc, err := locationBackend(home)
	if err != nil {
		return err
	}

	// git-mode files: deleting bytes out from under a repo must be loud.
	if file.SyncMode == catalog.SyncModeGit {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s is a git-mode file — repos referencing it will hard-fail on next pull until repushed\n", identity.Short(file.ID))
	}

	// Confirmation preflight (before the password): abortable without typing a
	// secret. A non-TTY run without --yes is refused.
	if !fl.yes {
		in := cmd.InOrStdin()
		if !isInteractive(in) {
			return tserr.ConfigErr("vault rm: refusing to delete "+home+"/"+file.Path+" without confirmation — pass --yes", nil)
		}
		pr := setup.NewStdinPrompter(in, cmd.OutOrStdout())
		ans, aerr := pr.AskString("delete "+home+"/"+file.Path+" (id "+identity.Short(file.ID)+")? [y/N]", "n")
		if aerr != nil {
			return tserr.ConfigErr("vault rm: read confirmation", aerr)
		}
		if !isYes(ans) {
			fmt.Fprintln(cmd.OutOrStdout(), "vault rm: aborted — nothing deleted")
			return nil
		}
	}

	// Password gate on the mutated node, before any intent (D9 / §16).
	if err := gateLocation(ctx, loc, b, home, fl.passwordFile); err != nil {
		return err
	}

	// WAL lifecycle: intent → (under the lock) last-referent check → delete blob →
	// remove catalog entry → done. The intent carries the deleted identity — the
	// WAL done-entry is the last audit trace of it.
	now := time.Now().UTC()
	opID := rmOpID(file.ID, file.SHA256)
	intent := wal.Entry{
		OpID:      opID,
		OpType:    wal.OpDelete,
		BlobRefs:  []string{file.ID},
		Actor:     initActor(cmd),
		CreatedAt: now,
		Args: map[string]string{
			"id":             file.ID,
			"path":           file.Path,
			"content_sha256": file.SHA256,
			"original_path":  file.Genesis.OriginalPath,
			"origin_node":    file.Genesis.OriginNode,
		},
	}
	log := &wal.Log{B: b}
	if err := appendOpIntent(ctx, log, intent, loc.Node, "rm"); err != nil {
		return err
	}

	// Re-read the catalog LIVE under the lock for the last-referent decision (the
	// WAL-as-lock serialization point — see the sha-vs-id note in the handoff).
	cat, err := readCatalog(ctx, b)
	if err != nil {
		return tserr.NodeOfflineErr(loc.Node, err)
	}
	if cat == nil {
		return tserr.ObjMissingErr(home+"/"+file.Path, nil)
	}

	// Content-addressed storage: delete the blob only when THIS entry is its last
	// referent on this node; otherwise drop only the catalog entry (another file
	// still needs the bytes).
	referents := 0
	for _, f := range cat.Files {
		if f.SHA256 == file.SHA256 {
			referents++
		}
	}
	blobDeleted := false
	if referents <= 1 {
		if err := b.Delete(ctx, "objects/"+file.SHA256); err != nil {
			return tserr.NodeNotWritableErr(loc.Node, err)
		}
		blobDeleted = true
	}
	cat.Remove(file.Path)
	if err := persistCatalog(ctx, b, cat, loc.Node); err != nil {
		return err
	}
	if err := log.MarkDone(ctx, opID); err != nil {
		return tserr.NodeOfflineErr(loc.Node, err)
	}

	out := rmJSON{
		ID: file.ID, Short: identity.Short(file.ID), Home: home, Path: file.Path,
		SHA256: file.SHA256, SyncMode: file.SyncMode, BlobDeleted: blobDeleted,
		Genesis: file.Genesis,
	}
	if fl.json {
		bj, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(bj))
	} else {
		printRmText(cmd, out)
	}
	return nil
}

func printRmText(cmd *cobra.Command, r rmJSON) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "removed    %s/%s\n", r.Home, r.Path)
	fmt.Fprintf(w, "id         %s\n", r.Short)
	fmt.Fprintf(w, "sha256     %s\n", shortHash(r.SHA256))
	if r.BlobDeleted {
		fmt.Fprintln(w, "blob       deleted (last referent on this node)")
	} else {
		fmt.Fprintln(w, "blob       kept (another entry still references this content)")
	}
	// If a pull receipt for this id exists, restore-identity remains possible
	// after a re-ingest — the deletion is not necessarily the end of the identity.
	if dir, derr := identity.DefaultReceiptDir(); derr == nil {
		if _, rerr := identity.ReadReceipt(dir, r.ID); rerr == nil {
			fmt.Fprintf(w, "receipt    a pull receipt exists — `tailvault vault restore-identity` can re-bind this id after a re-ingest\n")
		}
	}
}

// appendOpIntent appends a mutating op's intent, mapping the WAL sentinels to the
// command boundary uniformly for rm/sync-mode (mirrors mv's appendMoveIntent): a
// duplicate op id is an idempotent resume; an in-flight op on the blob is a
// transient config error; a broken chain is TV-FED; anything else is a node
// failure. op is the command name for the message ("rm" / "sync-mode").
func appendOpIntent(ctx context.Context, log *wal.Log, e wal.Entry, node, op string) error {
	if _, err := log.AppendIntent(ctx, e); err != nil {
		switch {
		case errors.Is(err, wal.ErrDuplicateOp):
			return nil // idempotent resume — Delete/Remove/MarkDone are all idempotent
		case errors.Is(err, wal.ErrOpInFlight):
			return tserr.ConfigErr("vault "+op+": another operation is in flight on this file — retry shortly", err)
		case errors.Is(err, wal.ErrChainBroken):
			return tserr.FedChainBrokenErr(node, err)
		default:
			return tserr.NodeOfflineErr(node, err)
		}
	}
	return nil
}

// opIDFromParts derives a DETERMINISTIC v4-shaped op id from a domain tag plus
// identifying parts, so a retry after a crash re-presents the SAME id and the WAL
// dedups (ErrDuplicateOp) instead of double-applying. NUL-joined so the parts are
// unambiguous.
func opIDFromParts(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	var b [16]byte
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return hex.EncodeToString(b[:])
}

func rmOpID(id, sha string) string { return opIDFromParts("tailvault/vault-rm", id, sha) }
