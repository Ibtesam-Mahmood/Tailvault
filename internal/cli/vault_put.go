package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/setup"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// newVaultPutCmd implements `tailvault vault put <local-file> <location>/<dest>`:
// ingestion path 3 (Push). It sends a local file into an active storage
// location from any tailnet machine, with no repo involved, running the full
// node-side WAL lifecycle (preflight → intent → blob → catalog → done) and
// minting the file's genesis identity.
//
// Ingestion is NOT password-gated (DEV-46.7): SPEC §16's frozen gated set
// enumerates exactly {mv, rm, sync_mode, remote gc, evict, roster, restore-
// identity} — creation is not on it. put rides the tailnet ACL + SSH like reads;
// the password protects destruction/relocation/governance, not new content.
// (task-51 revisits whether ingestion should join the gated set.)
func newVaultPutCmd() *cobra.Command {
	var onConflict, renameTo string
	var rmSource, jsonOut bool
	cmd := &cobra.Command{
		Use:   "put <local-file> <location>/<dest-path>",
		Short: "Ingest a local file into a storage location (no checkout)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultPut(cmd, args[0], args[1], putFlags{
				onConflict: onConflict, renameTo: renameTo, rmSource: rmSource, json: jsonOut,
			})
		},
	}
	cmd.Flags().StringVar(&onConflict, "on-conflict", "", "non-interactive conflict mode: copy|rename|stop")
	cmd.Flags().StringVar(&renameTo, "rename-to", "", "destination path for --on-conflict=rename")
	cmd.Flags().BoolVar(&rmSource, "rm-source", false, "delete the local source after verified success")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

type putFlags struct {
	onConflict string
	renameTo   string
	rmSource   bool
	json       bool
}

// putJSON is the scriptable --json contract.
type putJSON struct {
	ID            string `json:"id"`
	Short         string `json:"short_id"`
	Location      string `json:"location"`
	Path          string `json:"path"` // final dest rel path (may differ from requested on copy/rename)
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	SyncMode      string `json:"sync_mode"`
	Conflict      string `json:"conflict,omitempty"` // "copy" | "rename" when the dest was relocated
	SourceRemoved bool   `json:"source_removed"`
}

func runVaultPut(cmd *cobra.Command, localPath, destArg string, fl putFlags) error {
	ctx := cmd.Context()

	// Destination must be a logical path "<location>/<rel>", never a bare id.
	tgt, err := parseTarget(destArg)
	if err != nil {
		return err
	}
	if tgt.isID {
		return tserr.ConfigErr("vault put: destination must be <location>/<path>, not a file id", nil)
	}
	if tgt.rel == "" {
		return tserr.ConfigErr("vault put: destination needs a path within "+tgt.loc+" (e.g. "+tgt.loc+"/media/file.mp4)", nil)
	}
	locName, rel := tgt.loc, tgt.rel

	// Local source preflight (fail early, before touching the node).
	info, err := os.Stat(localPath)
	if err != nil {
		return tserr.ConfigErr("vault put: cannot read local file "+localPath, err)
	}
	if info.IsDir() {
		return tserr.ConfigErr("vault put: "+localPath+" is a directory (put one file at a time)", nil)
	}

	b, loc, err := locationBackend(locName)
	if err != nil {
		return err
	}

	// Conflict detection consults the LIVE destination catalog, never the client
	// cache (a stale cache could mask a ghost and silently overwrite — data loss).
	cat, err := readCatalog(ctx, b)
	if err != nil {
		return tserr.NodeOfflineErr(loc.Node, err) // transport/parse failure → node unreachable family
	}
	if cat == nil {
		return tserr.ConfigErr("vault put: "+locName+" is not initialised; run `tailvault vault init "+locName+"` on the node first", nil)
	}

	sha, size, err := hashLocalFile(localPath)
	if err != nil {
		return tserr.ConfigErr("vault put: hash "+localPath, err)
	}

	// Resolve any name conflict BEFORE the WAL intent (dry-run preflight, D6): a
	// doomed or aborted op must never acquire the lock.
	finalRel, conflict, abort, err := resolvePutConflict(cmd, cat, locName, rel, fl)
	if err != nil {
		return err
	}
	if abort {
		fmt.Fprintln(cmd.OutOrStdout(), "vault put: stopped — destination exists, nothing written")
		return nil
	}

	// Write-ahead ordering is load-bearing (SPEC §10): intent → blob → catalog →
	// done. A crash anywhere leaves a detectable, repairable state for verify/heal.
	now := time.Now().UTC()
	opID := putOpID(loc.Node, finalRel, sha) // deterministic → a retry re-presents the same id (idempotent)
	g := identity.Genesis{ContentSHA256: sha, OriginalPath: finalRel, IngestOpID: opID, OriginNode: loc.Node}
	id, err := identity.MintID(g)
	if err != nil {
		return tserr.ConfigErr("vault put: mint id", err)
	}

	entry := wal.Entry{
		OpID:      opID,
		OpType:    wal.OpIngest,
		BlobRefs:  []string{id},
		Actor:     initActor(cmd),
		CreatedAt: now,
		Args: map[string]string{
			"path":           finalRel,
			"content_sha256": sha,
			"origin_node":    loc.Node,
			"sync_mode":      catalog.SyncModeManual, // default for all ingestion paths (D21); git is git-flow only
			"size":           strconv.FormatInt(size, 10),
		},
	}
	log := &wal.Log{B: b}
	if _, err := log.AppendIntent(ctx, entry); err != nil {
		switch {
		case errors.Is(err, wal.ErrDuplicateOp):
			// Idempotent retry of an interrupted put: the intent is already recorded.
			// Fall through — Put (content-addressed), Upsert and MarkDone are all
			// idempotent, so re-running completes without minting a second identity.
		case errors.Is(err, wal.ErrOpInFlight):
			return tserr.ConfigErr("vault put: another operation is in flight on "+finalRel+" — retry shortly", err)
		case errors.Is(err, wal.ErrChainBroken):
			return tserr.FedChainBrokenErr(loc.Node, err)
		default:
			return tserr.NodeOfflineErr(loc.Node, err)
		}
	}

	// Stream the blob to the content-addressed store (Put dedups on Stat-hit).
	src, err := os.Open(localPath)
	if err != nil {
		return tserr.ConfigErr("vault put: open "+localPath, err)
	}
	if err := b.Put(ctx, "objects/"+sha, src); err != nil {
		_ = src.Close()
		return tserr.NodeNotWritableErr(loc.Node, err)
	}
	_ = src.Close()

	// Catalog update, then persist atomically (PutOverwrite of the mutable key).
	cat.Upsert(catalog.File{
		ID:          id,
		Genesis:     catalog.Genesis(g),
		SHA256:      sha,
		Path:        finalRel,
		SyncMode:    catalog.SyncModeManual,
		Size:        size,
		CreatedAt:   now,
		UpdatedAt:   now,
		LastScanned: now,
	})
	cbytes, err := catalog.Encode(cat)
	if err != nil {
		return tserr.ConfigErr("vault put: encode catalog", err)
	}
	if err := b.PutOverwrite(ctx, catalogStoreKey, bytes.NewReader(cbytes)); err != nil {
		return tserr.NodeNotWritableErr(loc.Node, err)
	}

	// WAL done only after the catalog is durable.
	if err := log.MarkDone(ctx, opID); err != nil {
		return tserr.NodeOfflineErr(loc.Node, err)
	}

	// Post-put integrity: the node must hold a blob that hashes to sha.
	got, err := b.HashObject(ctx, "objects/"+sha)
	if err != nil {
		return tserr.ObjMissingErr(identity.Short(id), err)
	}
	if got != sha {
		return tserr.ObjMissingErr(identity.Short(id)+" (post-put integrity: node holds "+shortHash(got)+", expected "+shortHash(sha)+")", nil)
	}

	// --rm-source deletes the local clone ONLY after the post-put check passes.
	removed := false
	if fl.rmSource {
		if err := os.Remove(localPath); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: put verified but could not remove --rm-source %s: %v\n", localPath, err)
		} else {
			removed = true
		}
	}

	out := putJSON{
		ID: id, Short: identity.Short(id), Location: locName, Path: finalRel,
		SHA256: sha, Size: size, SyncMode: catalog.SyncModeManual,
		Conflict: conflict, SourceRemoved: removed,
	}
	if fl.json {
		bj, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(bj))
	} else {
		printPutText(cmd, out, localPath, conflict)
	}
	return nil
}

// resolvePutConflict checks the live catalog for an existing entry at rel and
// applies the conflict policy. Returns the FINAL dest rel, the conflict mode that
// relocated it ("" if none), and abort=true for stop. With no --on-conflict and a
// non-TTY context, an existing destination is a hard-fail (never a silent guess).
func resolvePutConflict(cmd *cobra.Command, cat *catalog.Catalog, locName, rel string, fl putFlags) (finalRel, conflict string, abort bool, err error) {
	if _, exists := cat.Find(rel); !exists {
		return rel, "", false, nil
	}

	in := cmd.InOrStdin()
	mode := strings.ToLower(strings.TrimSpace(fl.onConflict))
	if mode == "" {
		if !isInteractive(in) {
			return "", "", false, tserr.ConfigErr("vault put: destination "+locName+"/"+rel+" already exists; pass --on-conflict=copy|rename|stop", nil)
		}
		pr := setup.NewStdinPrompter(in, cmd.OutOrStdout())
		ans, aerr := pr.AskString("destination "+locName+"/"+rel+" exists — [c]opy keep-both / [r]ename / [s]top?", "stop")
		if aerr != nil {
			return "", "", false, tserr.ConfigErr("vault put: read conflict choice", aerr)
		}
		mode = normalizeConflictMode(ans)
	}

	switch mode {
	case "stop":
		return "", "", true, nil
	case "copy":
		// Keep both: the vault entry is untouched; the local file lands under a
		// deduplicated name. The blob is content-addressed regardless — only the
		// logical path differs.
		return freeDedupPath(cat, rel), "copy", false, nil
	case "rename":
		newRel := strings.TrimSpace(fl.renameTo)
		if newRel == "" {
			if !isInteractive(in) {
				return "", "", false, tserr.ConfigErr("vault put: --on-conflict=rename needs --rename-to <dest> in a non-interactive context", nil)
			}
			pr := setup.NewStdinPrompter(in, cmd.OutOrStdout())
			ans, aerr := pr.AskString("new destination path within "+locName, "")
			if aerr != nil {
				return "", "", false, tserr.ConfigErr("vault put: read rename target", aerr)
			}
			newRel = strings.TrimSpace(ans)
		}
		if newRel == "" {
			return "", "", false, tserr.ConfigErr("vault put: rename target is empty", nil)
		}
		if _, exists := cat.Find(newRel); exists {
			return "", "", false, tserr.ConfigErr("vault put: rename target "+locName+"/"+newRel+" also exists", nil)
		}
		return newRel, "rename", false, nil
	default:
		return "", "", false, tserr.ConfigErr("vault put: invalid conflict mode "+mode+" (want copy|rename|stop)", nil)
	}
}

// normalizeConflictMode maps interactive single-letter / word answers to a mode.
func normalizeConflictMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "c", "copy":
		return "copy"
	case "r", "rename":
		return "rename"
	default:
		return "stop" // anything else (incl. blank/"s") is the safe default
	}
}

// freeDedupPath returns the first "<stem> (N)<ext>" (N≥2, slash-path aware) not
// already present in the catalog. The "(N)" style is a local convention — SPEC v2
// does not freeze a dedup-name format, so this is documented as a deviation.
func freeDedupPath(cat *catalog.Catalog, rel string) string {
	ext := path.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, n, ext)
		if _, ok := cat.Find(cand); !ok {
			return cand
		}
	}
}

// putOpID derives a DETERMINISTIC op id from (node, dest, content sha) so that a
// retry after a crash/network drop re-presents the SAME id — the WAL then dedups
// (ErrDuplicateOp) instead of minting a second identity for the same ingest. It
// returns a v4-shaped UUID (32 lowercase hex) for format parity with NewOpID.
func putOpID(node, rel, sha string) string {
	sum := sha256.Sum256([]byte("tailvault/vault-put-ingest\x00" + node + "\x00" + rel + "\x00" + sha))
	var b [16]byte
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return hex.EncodeToString(b[:])
}

// hashLocalFile streams a local file through sha256, returning its hex digest and
// byte size. Constant memory; fine at ~1 GB.
func hashLocalFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// isInteractive reports whether the command's input is a terminal — the gate for
// prompting on a conflict. A non-TTY context without an explicit --on-conflict is
// a hard-fail (never a silent guess). Detection is on the actual input reader (not
// the os.Stdin global) so a piped/redirected/test input is correctly non-TTY: only
// a *os.File backed by a character device counts as interactive.
func isInteractive(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func printPutText(cmd *cobra.Command, p putJSON, localPath, conflict string) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "put        %s -> %s/%s\n", localPath, p.Location, p.Path)
	if conflict == "copy" {
		fmt.Fprintf(w, "conflict   destination existed — stored as a copy under %q\n", p.Path)
	} else if conflict == "rename" {
		fmt.Fprintf(w, "conflict   destination existed — renamed to %q\n", p.Path)
	}
	fmt.Fprintf(w, "id         %s\n", p.Short)
	fmt.Fprintf(w, "sha256     %s\n", shortHash(p.SHA256))
	fmt.Fprintf(w, "size       %s\n", humanBytes(p.Size))
	fmt.Fprintf(w, "sync_mode  %s\n", p.SyncMode)
	if p.SourceRemoved {
		fmt.Fprintln(w, "source     removed (--rm-source; verified)")
	} else {
		fmt.Fprintln(w, "source     the vault copy is now the original; the local file is a deletable clone")
	}
}
