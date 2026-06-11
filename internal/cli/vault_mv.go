package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/setup"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// newVaultMvCmd implements `tailvault vault mv <src logical-path|id> <dest
// location>/<dest-path>`: relocate a federated file, either INTRA-location (a
// rename within one node's tree — a catalog path change, no bytes move) or
// CROSS-location (bytes travel node-to-node over the tailnet). The file ID NEVER
// changes — only the logical path does (D19 dual addressing). Git repos that
// referenced the old home discover the move on next sync (pull warns; `heal`
// rewrites the lock).
//
// A move mutates one or two nodes, so it is PASSWORD-GATED on every node it
// touches (D9 / SPEC v2 §16) — gateLocation runs before any WAL intent. A
// cross-location move is the system's first two-node mutation: the WAL lock is
// taken on BOTH ends, and the single-home invariant holds throughout — the dest
// entry only becomes live after verification, and the source entry becomes a
// `moved_to` forwarder in the same lifecycle (never two live homes, never zero).
func newVaultMvCmd() *cobra.Command {
	var onConflict, renameTo, passwordFile string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "mv <src logical-path | id> <dest location>/<dest-path>",
		Short: "Move a federated file within or between locations (ID preserved)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultMv(cmd, args[0], args[1], mvFlags{
				onConflict: onConflict, renameTo: renameTo, passwordFile: passwordFile, json: jsonOut,
			})
		},
	}
	cmd.Flags().StringVar(&onConflict, "on-conflict", "", "non-interactive dest-collision mode: copy|rename|stop")
	cmd.Flags().StringVar(&renameTo, "rename-to", "", "destination path for --on-conflict=rename")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "read the vault password from this file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return markGated(cmd)
}

type mvFlags struct {
	onConflict   string
	renameTo     string
	passwordFile string
	json         bool
}

// mvJSON is the scriptable --json contract.
type mvJSON struct {
	ID       string `json:"id"`
	Short    string `json:"short_id"`
	Kind     string `json:"kind"` // "intra" | "cross"
	From     string `json:"from"` // "<member>/<path>"
	To       string `json:"to"`   // "<member>/<path>"
	SHA256   string `json:"sha256"`
	SyncMode string `json:"sync_mode"`
	Conflict string `json:"conflict,omitempty"` // "copy" | "rename" when the dest path was relocated
	MovedTo  string `json:"moved_to,omitempty"` // forwarding member left at the source (cross only)
}

func runVaultMv(cmd *cobra.Command, srcArg, destArg string, fl mvFlags) error {
	ctx := cmd.Context()

	reg, err := locations.Load()
	if err != nil {
		return tserr.ConfigErr("load locations.toml", err)
	}
	roster, err := loadRoster(ctx, reg)
	if err != nil {
		return err
	}

	// Destination is always a logical path "<location>/<rel>", never a bare id.
	dtgt, err := parseTarget(destArg)
	if err != nil {
		return err
	}
	if dtgt.isID {
		return tserr.ConfigErr("vault mv: destination must be <location>/<path>, not a file id", nil)
	}
	if dtgt.rel == "" {
		return tserr.ConfigErr("vault mv: destination needs a path within "+dtgt.loc+" (e.g. "+dtgt.loc+"/media/file.mp4)", nil)
	}
	destLoc, destRel := dtgt.loc, dtgt.rel

	// Resolve the source to its CURRENT home (the resolver follows moved_to /
	// fan-out), so a mv of an already-relocated file acts on the live copy.
	file, srcMember, err := resolveSource(ctx, reg, roster, srcArg)
	if err != nil {
		return err
	}

	intra := destLoc == srcMember

	srcBackend, srcLoc, err := locationBackend(srcMember)
	if err != nil {
		return err
	}
	destBackend, destLocRec, err := locationBackend(destLoc)
	if err != nil {
		return err
	}

	// Dest catalog (LIVE) drives the conflict check and, for cross moves, the
	// not-initialised guard. For an intra move src and dest are the same catalog.
	destCat, err := readCatalog(ctx, destBackend)
	if err != nil {
		return tserr.NodeOfflineErr(destLocRec.Node, err)
	}
	if destCat == nil {
		if intra {
			return tserr.ObjMissingErr(srcMember+"/"+file.Path, nil)
		}
		return tserr.ConfigErr("vault mv: "+destLoc+" is not initialised; run `tailvault vault init "+destLoc+"` on the node first", nil)
	}

	// Resolve any dest-name collision BEFORE any gate/intent (dry-run preflight,
	// D6): a doomed or aborted move must never take the lock or prompt for a
	// password. For an intra move, a self-move (dest == current path) is a no-op.
	if intra && destRel == file.Path {
		return tserr.ConfigErr("vault mv: source and destination are the same path "+srcMember+"/"+destRel, nil)
	}
	finalRel, conflict, abort, err := resolveMvConflict(cmd, destCat, destLoc, destRel, fl)
	if err != nil {
		return err
	}
	if abort {
		fmt.Fprintln(cmd.OutOrStdout(), "vault mv: stopped — destination exists, nothing moved")
		return nil
	}

	// Password gate on EVERY mutated node, before any intent (D9 / §16). Order:
	// source first, then destination for a cross move. A rejection on either node
	// leaves zero intents anywhere.
	if err := gateLocation(ctx, srcLoc, srcBackend, srcMember, fl.passwordFile); err != nil {
		return err
	}
	if !intra {
		if err := gateLocation(ctx, destLocRec, destBackend, destLoc, fl.passwordFile); err != nil {
			return err
		}
	}

	if intra {
		return mvIntra(ctx, cmd, mvCtx{
			file: file, member: srcMember, b: srcBackend, loc: srcLoc,
			finalRel: finalRel, conflict: conflict, fl: fl,
		})
	}
	return mvCross(ctx, cmd, mvCrossCtx{
		file: file, srcMember: srcMember, srcBackend: srcBackend, srcLoc: srcLoc,
		destMember: destLoc, destBackend: destBackend, destLoc: destLocRec, destCat: destCat,
		finalRel: finalRel, conflict: conflict, fl: fl,
	})
}

// resolveSource looks up the move source and confirms its live home through the
// resolution engine (following moved_to / fan-out, mapping PartialView→TV-FED,
// Missing→TV-OBJ exactly as the read commands do). Returns the authoritative
// file record and the member that currently holds it.
func resolveSource(ctx context.Context, reg locations.Registry, roster fed.Roster, arg string) (catalog.File, string, error) {
	tgt, err := parseTarget(arg)
	if err != nil {
		return catalog.File{}, "", err
	}
	var file catalog.File
	var home string
	if tgt.isID {
		file, home, err = fileByIDPrefix(ctx, reg, roster, tgt.id)
	} else {
		file, home, err = fileByPath(ctx, reg, tgt.loc, tgt.rel)
	}
	if err != nil {
		return catalog.File{}, "", err
	}

	resolver := &fed.Resolver{
		Roster: roster,
		Q:      fed.NewBackendQuerier(backendForRegistry(reg)),
		Probe:  memberProbe(reg),
	}
	res, err := resolver.Resolve(ctx, file.ID, home)
	if err != nil {
		if errors.Is(err, wal.ErrChainBroken) {
			return catalog.File{}, "", tserr.FedChainBrokenErr(home, err)
		}
		return catalog.File{}, "", err
	}
	if _, oerr := resolveOutcome(res, file.ID); oerr != nil {
		return catalog.File{}, "", oerr
	}
	if res.View.Found {
		file = res.View.File
		home = res.View.Member
	}
	return file, home, nil
}

type mvCtx struct {
	file     catalog.File
	member   string
	b        backend.Backend
	loc      locations.Location
	finalRel string
	conflict string
	fl       mvFlags
}

// mvIntra renames a file within one member's tree: a single WAL intent, a catalog
// path rewrite, no transfer. No `moved_to` is left behind — the entry itself now
// records the new path, and since the home member did not change, resolution
// still finds it. Stale locks referencing the old path get the WARN+heal flow.
func mvIntra(ctx context.Context, cmd *cobra.Command, m mvCtx) error {
	now := time.Now().UTC()
	opID := moveOpID(m.file.ID, m.member, m.member, m.finalRel)
	log := &wal.Log{B: m.b}
	entry := wal.Entry{
		OpID:      opID,
		OpType:    wal.OpMove,
		BlobRefs:  []string{m.file.ID},
		Actor:     initActor(cmd),
		CreatedAt: now,
		Args: map[string]string{
			"from":   m.file.Path,
			"to":     m.finalRel,
			"member": m.member,
		},
	}
	if err := appendMoveIntent(ctx, log, entry, m.loc.Node); err != nil {
		return err
	}

	cat, err := readCatalog(ctx, m.b)
	if err != nil {
		return tserr.NodeOfflineErr(m.loc.Node, err)
	}
	if cat == nil {
		return tserr.ObjMissingErr(m.member+"/"+m.file.Path, nil)
	}
	moved := m.file
	moved.Path = m.finalRel
	moved.UpdatedAt = now
	if err := assertIDInvariant(m.file, moved); err != nil {
		return err
	}
	cat.Remove(m.file.Path)
	cat.Upsert(moved)
	if err := persistCatalog(ctx, m.b, cat, m.loc.Node); err != nil {
		return err
	}
	if err := log.MarkDone(ctx, opID); err != nil {
		return tserr.NodeOfflineErr(m.loc.Node, err)
	}

	out := mvJSON{
		ID: m.file.ID, Short: identity.Short(m.file.ID), Kind: "intra",
		From: m.member + "/" + m.file.Path, To: m.member + "/" + m.finalRel,
		SHA256: m.file.SHA256, SyncMode: m.file.SyncMode, Conflict: m.conflict,
	}
	return emitMv(cmd, out, m.fl.json)
}

type mvCrossCtx struct {
	file        catalog.File
	srcMember   string
	srcBackend  backend.Backend
	srcLoc      locations.Location
	destMember  string
	destBackend backend.Backend
	destLoc     locations.Location
	destCat     *catalog.Catalog
	finalRel    string
	conflict    string
	fl          mvFlags
}

// mvCross moves a file between two members. The lifecycle is ordered so the
// single-home invariant holds at every step (SPEC v2 §10): source intent → dest
// intent → node-to-node transfer → verify at dest → dest catalog add → dest done
// → source catalog demoted to a `moved_to` forwarder → source done. A failure
// mid-way leaves pending intents (visible to `ops`) and is retryable — never a
// half-error, never two live homes.
func mvCross(ctx context.Context, cmd *cobra.Command, m mvCrossCtx) error {
	now := time.Now().UTC()
	opID := moveOpID(m.file.ID, m.srcMember, m.destMember, m.finalRel)
	srcLog := &wal.Log{B: m.srcBackend}
	destLog := &wal.Log{B: m.destBackend}

	// (1) Source intent — carries the moved_to forwarding pointer (the DESTINATION
	// MEMBER NAME) so the completed record on the source forwards resolution even
	// when the new home is offline. BlobRefs=[id] locks the blob on the source.
	srcIntent := wal.Entry{
		OpID:      opID,
		OpType:    wal.OpMove,
		BlobRefs:  []string{m.file.ID},
		Actor:     initActor(cmd),
		CreatedAt: now,
		Args: map[string]string{
			"from":           m.srcMember,
			"to":             m.destMember,
			"moved_to":       m.destMember, // forwarding pointer (member name); read by the resolver
			"src_path":       m.file.Path,
			"dest_path":      m.finalRel,
			"content_sha256": m.file.SHA256,
		},
	}
	if err := appendMoveIntent(ctx, srcLog, srcIntent, m.srcLoc.Node); err != nil {
		return err
	}

	// (2) Destination intent — same op id (a different WAL on a different node, so
	// no collision); locks the blob on the dest. No moved_to here: the dest is the
	// new home, not a forwarder.
	destIntent := wal.Entry{
		OpID:      opID,
		OpType:    wal.OpMove,
		BlobRefs:  []string{m.file.ID},
		Actor:     initActor(cmd),
		CreatedAt: now,
		// The cross-moved file appears on the dest node ONLY via this record (there
		// is no OpIngest for it here), so it must carry the FULL genesis preimage —
		// id + the 4-field genesis + sha + dest_path — for heal's ProjectCatalog to
		// rebuild the dest entry from the WAL alone (fix-35-D / projection-sufficiency).
		Args: map[string]string{
			"id":             m.file.ID,
			"from":           m.srcMember,
			"src_path":       m.file.Path,
			"dest_path":      m.finalRel,
			"content_sha256": m.file.SHA256,
			"original_path":  m.file.Genesis.OriginalPath,
			"ingest_op_id":   m.file.Genesis.IngestOpID,
			"origin_node":    m.file.Genesis.OriginNode,
		},
	}
	if err := appendMoveIntent(ctx, destLog, destIntent, m.destLoc.Node); err != nil {
		return err
	}

	// (3) Node-to-node transfer: bytes flow source→dest directly (peer-to-peer),
	// never relayed through the client (backend.Transfer enforces this — there is
	// no client-relay fallback).
	objKey := "objects/" + m.file.SHA256
	if err := backend.Transfer(ctx, m.srcBackend, m.destBackend, objKey); err != nil {
		return err // already a typed TV-OBJ / TV-NODE error from the backend
	}

	// (4) Verify at dest BEFORE cut-over. A content-addressed (git/non-manual)
	// mode MUST hash to the recorded sha — a mismatch is corruption, hard-fail
	// (pending intents remain, nothing cut over). A manual file may legitimately
	// have drifted since its last scan (H12): the move re-hashes and re-homes it
	// under its true hash, counting as a fresh scan.
	destHash, err := m.destBackend.HashObject(ctx, objKey)
	if err != nil {
		return tserr.ObjMissingErr(identity.Short(m.file.ID), err)
	}
	newSHA := m.file.SHA256
	lastScanned := m.file.LastScanned
	if destHash != m.file.SHA256 {
		if m.file.SyncMode != catalog.SyncModeManual {
			return tserr.ObjMissingErr(identity.Short(m.file.ID)+" (move integrity: dest holds "+shortHash(destHash)+", expected "+shortHash(m.file.SHA256)+")", nil)
		}
		// Manual drift: re-home the bytes under their TRUE hash so the entry stays
		// content-addressed (`vault get` keys by the catalog sha) and bump
		// last_scanned — the move doubles as a scan.
		if err := rehomeDriftedObject(ctx, m.destBackend, m.file.SHA256, destHash, m.destLoc.Node); err != nil {
			return err
		}
		newSHA = destHash
		lastScanned = now
	}

	// (5) Dest catalog add — the ID and full genesis record are carried over
	// UNCHANGED (a move never re-mints, D19); only the path (and, on drift, the
	// sha/last_scanned) differs.
	size := m.file.Size
	if meta, serr := m.destBackend.Stat(ctx, "objects/"+newSHA); serr == nil && meta.Exists {
		size = meta.Size
	}
	moved := catalog.File{
		ID: m.file.ID, Genesis: m.file.Genesis, SHA256: newSHA, Path: m.finalRel,
		SyncMode: m.file.SyncMode, Size: size,
		CreatedAt: m.file.CreatedAt, UpdatedAt: now, LastScanned: lastScanned,
	}
	if err := assertIDInvariant(m.file, moved); err != nil {
		return err
	}
	m.destCat.Upsert(moved)
	if err := persistCatalog(ctx, m.destBackend, m.destCat, m.destLoc.Node); err != nil {
		return err
	}
	// (6) Dest done — the file is now live at its new home.
	if err := destLog.MarkDone(ctx, opID); err != nil {
		return tserr.NodeOfflineErr(m.destLoc.Node, err)
	}

	// (7) Source catalog: drop the entry. The forwarding pointer is the source's
	// DONE move record (args["moved_to"]) — the resolver reads it from the WAL, so
	// the catalog entry itself is removed (the blob is no longer authoritative
	// here; the dest entry is). Re-read live to avoid clobbering concurrent edits.
	srcCat, err := readCatalog(ctx, m.srcBackend)
	if err != nil {
		return tserr.NodeOfflineErr(m.srcLoc.Node, err)
	}
	if srcCat != nil {
		srcCat.Remove(m.file.Path)
		if err := persistCatalog(ctx, m.srcBackend, srcCat, m.srcLoc.Node); err != nil {
			return err
		}
	}
	// (8) Source done — flips the source intent to a forwarding record.
	if err := srcLog.MarkDone(ctx, opID); err != nil {
		return tserr.NodeOfflineErr(m.srcLoc.Node, err)
	}

	out := mvJSON{
		ID: m.file.ID, Short: identity.Short(m.file.ID), Kind: "cross",
		From: m.srcMember + "/" + m.file.Path, To: m.destMember + "/" + m.finalRel,
		SHA256: newSHA, SyncMode: m.file.SyncMode, Conflict: m.conflict, MovedTo: m.destMember,
	}
	return emitMv(cmd, out, m.fl.json)
}

// rehomeDriftedObject copies a drifted manual object to its true-hash key on the
// destination so the catalog entry (which records the fresh hash) stays
// content-addressed. The transiently-keyed object under the old sha is left for
// GC to reclaim (it is unreferenced unless another entry shares that sha). The
// copy is buffered through the client here — acceptable for the rare drift path;
// a node-side copy primitive is a future optimization.
func rehomeDriftedObject(ctx context.Context, b backend.Backend, oldSHA, newSHA, node string) error {
	var buf bytes.Buffer
	if err := b.Get(ctx, "objects/"+oldSHA, &buf); err != nil {
		return err
	}
	if err := b.Put(ctx, "objects/"+newSHA, bytes.NewReader(buf.Bytes())); err != nil {
		return tserr.NodeNotWritableErr(node, err)
	}
	return nil
}

// appendMoveIntent appends a move intent, mapping the WAL sentinels to the
// command boundary: a duplicate op id is an idempotent resume (fall through); an
// in-flight op on the blob is a transient config error (retry); a broken chain is
// TV-FED; anything else is a node failure.
func appendMoveIntent(ctx context.Context, log *wal.Log, e wal.Entry, node string) error {
	if _, err := log.AppendIntent(ctx, e); err != nil {
		switch {
		case errors.Is(err, wal.ErrDuplicateOp):
			return nil // idempotent resume — Transfer/Upsert/MarkDone are all idempotent
		case errors.Is(err, wal.ErrOpInFlight):
			return tserr.ConfigErr("vault mv: another operation is in flight on this file — retry shortly", err)
		case errors.Is(err, wal.ErrChainBroken):
			return tserr.FedChainBrokenErr(node, err)
		default:
			return tserr.NodeOfflineErr(node, err)
		}
	}
	return nil
}

// persistCatalog encodes and atomically overwrites a member's catalog.
func persistCatalog(ctx context.Context, b backend.Backend, cat *catalog.Catalog, node string) error {
	enc, err := catalog.Encode(cat)
	if err != nil {
		return tserr.ConfigErr("vault mv: encode catalog", err)
	}
	if err := b.PutOverwrite(ctx, catalogStoreKey, bytes.NewReader(enc)); err != nil {
		return tserr.NodeNotWritableErr(node, err)
	}
	return nil
}

// assertIDInvariant defends the D19 contract in code: a move must never change a
// file's id or genesis record. A violation is an internal bug, surfaced loudly
// rather than written to a catalog.
func assertIDInvariant(before, after catalog.File) error {
	if before.ID != after.ID || before.Genesis != after.Genesis {
		return fmt.Errorf("vault mv: internal error — move altered identity (id %s→%s)", identity.Short(before.ID), identity.Short(after.ID))
	}
	return nil
}

// resolveMvConflict applies the dest-collision policy, reusing put's primitives
// (freeDedupPath / isInteractive). Returns the final dest rel, the mode that
// relocated it ("" if none), and abort=true for stop. With no --on-conflict in a
// non-TTY context, an existing destination is a hard-fail (never a silent guess).
func resolveMvConflict(cmd *cobra.Command, cat *catalog.Catalog, locName, rel string, fl mvFlags) (finalRel, conflict string, abort bool, err error) {
	if _, exists := cat.Find(rel); !exists {
		return rel, "", false, nil
	}
	in := cmd.InOrStdin()
	mode := strings.ToLower(strings.TrimSpace(fl.onConflict))
	if mode == "" {
		if !isInteractive(in) {
			return "", "", false, tserr.ConfigErr("vault mv: destination "+locName+"/"+rel+" already exists; pass --on-conflict=copy|rename|stop", nil)
		}
		pr := setup.NewStdinPrompter(in, cmd.OutOrStdout())
		ans, aerr := pr.AskString("destination "+locName+"/"+rel+" exists — [c]opy keep-both / [r]ename / [s]top?", "stop")
		if aerr != nil {
			return "", "", false, tserr.ConfigErr("vault mv: read conflict choice", aerr)
		}
		mode = normalizeConflictMode(ans)
	}
	switch mode {
	case "stop":
		return "", "", true, nil
	case "copy":
		return freeDedupPath(cat, rel), "copy", false, nil
	case "rename":
		newRel := strings.TrimSpace(fl.renameTo)
		if newRel == "" {
			if !isInteractive(in) {
				return "", "", false, tserr.ConfigErr("vault mv: --on-conflict=rename needs --rename-to <dest> in a non-interactive context", nil)
			}
			pr := setup.NewStdinPrompter(in, cmd.OutOrStdout())
			ans, aerr := pr.AskString("new destination path within "+locName, "")
			if aerr != nil {
				return "", "", false, tserr.ConfigErr("vault mv: read rename target", aerr)
			}
			newRel = strings.TrimSpace(ans)
		}
		if newRel == "" {
			return "", "", false, tserr.ConfigErr("vault mv: rename target is empty", nil)
		}
		if _, exists := cat.Find(newRel); exists {
			return "", "", false, tserr.ConfigErr("vault mv: rename target "+locName+"/"+newRel+" also exists", nil)
		}
		return newRel, "rename", false, nil
	default:
		return "", "", false, tserr.ConfigErr("vault mv: invalid conflict mode "+mode+" (want copy|rename|stop)", nil)
	}
}

// moveOpID derives a DETERMINISTIC op id from (id, src member, dest member, dest
// path) so a retry after a crash re-presents the SAME id — the WAL then dedups
// (ErrDuplicateOp) and the command resumes idempotently instead of starting a
// second move. The same id is used on both ends of a cross move (distinct WALs on
// distinct nodes, so no collision). Returns a v4-shaped UUID for parity with
// NewOpID.
func moveOpID(id, src, dst, destPath string) string {
	sum := sha256.Sum256([]byte("tailvault/vault-mv\x00" + id + "\x00" + src + "\x00" + dst + "\x00" + destPath))
	var b [16]byte
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return hex.EncodeToString(b[:])
}

func emitMv(cmd *cobra.Command, m mvJSON, jsonOut bool) error {
	if jsonOut {
		bj, _ := json.MarshalIndent(m, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(bj))
		return nil
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "moved      %s -> %s (%s)\n", m.From, m.To, m.Kind)
	if m.Conflict == "copy" {
		fmt.Fprintf(w, "conflict   destination existed — moved to a copy name %q\n", m.To)
	} else if m.Conflict == "rename" {
		fmt.Fprintf(w, "conflict   destination existed — renamed to %q\n", m.To)
	}
	fmt.Fprintf(w, "id         %s\n", m.Short)
	fmt.Fprintf(w, "sha256     %s\n", shortHash(m.SHA256))
	fmt.Fprintf(w, "sync_mode  %s\n", m.SyncMode)
	if m.MovedTo != "" {
		fmt.Fprintf(w, "forwarder  source now points to %s (found even if %s is offline)\n", m.MovedTo, m.MovedTo)
	}
	return nil
}
