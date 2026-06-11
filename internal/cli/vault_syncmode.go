package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// settableSyncModes is the set of sync modes a user may set via the CLI. The
// catalog enum is OPEN (D15) — unknown/future values from federation skew are
// preserved on round-trip and treated as not-git by gc — but a USER may only flip
// a file between the day-one modes here. There is no catalog.ValidModes() (the
// catalog deliberately validates sync_mode against no closed list); a new mode
// becomes settable by adding it here.
var settableSyncModes = []string{catalog.SyncModeGit, catalog.SyncModeManual}

// newVaultSyncModeCmd implements `tailvault vault sync-mode <logical-path | id>
// <mode>`: flip a federated file's sync mode remotely. `git → manual` shields the
// file from gc forever and makes it editable in place; `manual → git` makes it a
// gc candidate again and stamps a fresh node-side re-hash + last_scanned (gc and
// verify must never reason from a stale sha on a freshly-git file).
//
// A flip mutates a node, so it is PASSWORD-GATED (D9 / §16) and rides the same
// WAL-as-lock lifecycle as put/mv/rm. Setting the current mode is an idempotent
// no-op success.
func newVaultSyncModeCmd() *cobra.Command {
	var jsonOut bool
	var passwordFile string
	cmd := &cobra.Command{
		Use:   "sync-mode <logical-path | id> <" + strings.Join(settableSyncModes, "|") + ">",
		Short: "Change a file's sync mode (git ⇄ manual) remotely",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultSyncMode(cmd, args[0], args[1], syncFlags{passwordFile: passwordFile, json: jsonOut})
		},
	}
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "read the vault password from this file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

type syncFlags struct {
	passwordFile string
	json         bool
}

// syncJSON is the scriptable --json contract.
type syncJSON struct {
	ID       string `json:"id"`
	Short    string `json:"short_id"`
	Home     string `json:"home"`
	Path     string `json:"path"`
	FromMode string `json:"from_mode"`
	ToMode   string `json:"to_mode"`
	SHA256   string `json:"sha256"`    // fresh re-hash when flipping to git, else unchanged
	Rehashed bool   `json:"rehashed"`  // true when → git triggered a node-side re-hash
	NoOp     bool   `json:"no_op"`     // true when the file was already in the requested mode
	GCExempt bool   `json:"gc_exempt"` // true when the resulting mode is non-git
}

func runVaultSyncMode(cmd *cobra.Command, arg, modeArg string, fl syncFlags) error {
	ctx := cmd.Context()

	mode := strings.ToLower(strings.TrimSpace(modeArg))
	if !isSettableMode(mode) {
		return tserr.ConfigErr("vault sync-mode: unknown mode "+modeArg+" (settable: "+strings.Join(settableSyncModes, ", ")+")", nil)
	}

	reg, err := locations.Load()
	if err != nil {
		return tserr.ConfigErr("load locations.toml", err)
	}
	roster, err := loadRoster(ctx, reg)
	if err != nil {
		return err
	}
	file, home, err := resolveSource(ctx, reg, roster, arg)
	if err != nil {
		return err
	}
	b, loc, err := locationBackend(home)
	if err != nil {
		return err
	}

	// Setting the current mode is an idempotent no-op success (no gate, no intent).
	if file.SyncMode == mode {
		out := syncJSON{
			ID: file.ID, Short: identity.Short(file.ID), Home: home, Path: file.Path,
			FromMode: mode, ToMode: mode, SHA256: file.SHA256, NoOp: true,
			GCExempt: mode != catalog.SyncModeGit,
		}
		if fl.json {
			bj, _ := json.MarshalIndent(out, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(bj))
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "sync-mode  %s/%s already %s — no change\n", home, file.Path, mode)
		}
		return nil
	}

	// Password gate on the mutated node, before any intent (D9 / §16).
	if err := gateLocation(ctx, loc, b, home, fl.passwordFile); err != nil {
		return err
	}

	now := time.Now().UTC()

	// Read the live entry and compute the resulting sha/last_scanned BEFORE the WAL
	// intent. A WAL entry is immutable (no post-hoc state, §10), so the OpSyncMode
	// record must carry its FULL effect — including the re-hashed new_sha256 and
	// last_scanned of a drifted manual→git flip — for heal's ProjectCatalog (a pure
	// function that cannot re-hash) to replay it (fix-35-D / projection-sufficiency).
	// HashObject is read-only, so computing it before the intent does not race the
	// lock; the only mutation (rehome) stays after the intent.
	cat, err := readCatalog(ctx, b)
	if err != nil {
		return tserr.NodeOfflineErr(loc.Node, err)
	}
	if cat == nil {
		return tserr.ObjMissingErr(home+"/"+file.Path, nil)
	}
	entry, ok := cat.Find(file.Path)
	if !ok {
		return tserr.ObjMissingErr(home+"/"+file.Path, nil)
	}

	newSHA := entry.SHA256
	lastScanned := entry.LastScanned
	rehashed := false
	needRehome := false
	if mode == catalog.SyncModeGit {
		// manual → git: re-hash on the node so gc/verify never see a stale sha. A
		// drifted manual file (true content hash != recorded sha) adopts its true
		// hash and is re-homed under it (content-addressed). last_scanned is stamped
		// either way — the flip counts as a fresh scan.
		trueHash, herr := b.HashObject(ctx, "objects/"+entry.SHA256)
		if herr != nil {
			return tserr.ObjMissingErr(identity.Short(file.ID), herr)
		}
		if trueHash != entry.SHA256 {
			newSHA = trueHash
			needRehome = true
		}
		lastScanned = now
		rehashed = true
	}

	opID := syncOpID(file.ID, mode)
	intent := wal.Entry{
		OpID:      opID,
		OpType:    wal.OpSyncMode,
		BlobRefs:  []string{file.ID},
		Actor:     initActor(cmd),
		CreatedAt: now,
		Args: map[string]string{
			"id":           file.ID,
			"path":         file.Path,
			"from_mode":    file.SyncMode,
			"to_mode":      mode,
			"new_sha256":   newSHA, // resulting sha (= old unless a drift re-hash); projectable
			"last_scanned": rfc(lastScanned),
		},
	}
	log := &wal.Log{B: b}
	if err := appendOpIntent(ctx, log, intent, loc.Node, "sync-mode"); err != nil {
		return err
	}

	if needRehome {
		if err := rehomeDriftedObject(ctx, b, entry.SHA256, newSHA, loc.Node); err != nil {
			return err
		}
	}

	updated := entry
	updated.SyncMode = mode
	updated.SHA256 = newSHA
	updated.LastScanned = lastScanned
	updated.UpdatedAt = now
	if err := assertIDInvariant(entry, updated); err != nil {
		return err
	}
	cat.Upsert(updated)
	if err := persistCatalog(ctx, b, cat, loc.Node); err != nil {
		return err
	}
	if err := log.MarkDone(ctx, opID); err != nil {
		return tserr.NodeOfflineErr(loc.Node, err)
	}

	out := syncJSON{
		ID: file.ID, Short: identity.Short(file.ID), Home: home, Path: file.Path,
		FromMode: file.SyncMode, ToMode: mode, SHA256: newSHA, Rehashed: rehashed,
		GCExempt: mode != catalog.SyncModeGit,
	}
	if fl.json {
		bj, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(bj))
	} else {
		printSyncText(cmd, out)
	}
	return nil
}

func printSyncText(cmd *cobra.Command, s syncJSON) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "sync-mode  %s/%s: %s -> %s\n", s.Home, s.Path, s.FromMode, s.ToMode)
	fmt.Fprintf(w, "id         %s\n", s.Short)
	if s.Rehashed {
		fmt.Fprintf(w, "rehash     re-hashed on the node; sha %s, last_scanned stamped — now a gc candidate\n", shortHash(s.SHA256))
	} else {
		fmt.Fprintln(w, "gc         exempt — manual files are never collected and are editable in place")
	}
}

func isSettableMode(mode string) bool {
	for _, m := range settableSyncModes {
		if m == mode {
			return true
		}
	}
	return false
}

func syncOpID(id, mode string) string { return opIDFromParts("tailvault/vault-sync-mode", id, mode) }
