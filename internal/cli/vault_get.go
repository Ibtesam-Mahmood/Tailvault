package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// newVaultGetCmd implements `tailvault vault get <logical-path | id>`: download a
// federated file to the local machine with no repo checkout, from whichever
// member currently holds it. Read-only — no password is ever requested (D9 /
// SPEC v2 §16). Every successful get verifies integrity and writes a pull
// receipt (an off-node identity backup, D24b).
func newVaultGetCmd() *cobra.Command {
	var out string
	var force, noReceipt, jsonOut bool
	cmd := &cobra.Command{
		Use:   "get <logical-path | id>",
		Short: "Download a federated file by path or ID (no checkout, no password)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultGet(cmd, args[0], getFlags{out: out, force: force, noReceipt: noReceipt, json: jsonOut})
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "local destination path (default: basename into cwd)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing local destination")
	cmd.Flags().BoolVar(&noReceipt, "no-receipt", false, "skip writing the pull receipt (debugging)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

type getFlags struct {
	out       string
	force     bool
	noReceipt bool
	json      bool
}

// getJSON is the scriptable --json contract.
type getJSON struct {
	ID          string   `json:"id"`
	Short       string   `json:"short_id"`
	Path        string   `json:"path"`
	Home        string   `json:"home"`
	Output      string   `json:"output"`
	SyncMode    string   `json:"sync_mode"`
	Size        int64    `json:"size"`
	SHA256      string   `json:"sha256"`            // hash of the bytes actually delivered
	Freshness   string   `json:"freshness"`         // git: "verified"; manual: scan-freshness
	Receipt     string   `json:"receipt,omitempty"` // receipt file path ("" if --no-receipt)
	Answered    []string `json:"members_answered"`
	Unreachable []string `json:"members_unreachable"`
	MovedHome   bool     `json:"moved_home"`
}

func runVaultGet(cmd *cobra.Command, arg string, fl getFlags) error {
	ctx := cmd.Context()
	reg, err := locations.Load()
	if err != nil {
		return tserr.ConfigErr("load locations.toml", err)
	}
	roster, err := loadRoster(ctx, reg)
	if err != nil {
		return err
	}
	tgt, err := parseTarget(arg)
	if err != nil {
		return err
	}

	var file catalog.File
	var home string
	if tgt.isID {
		file, home, err = fileByIDPrefix(ctx, reg, roster, tgt.id)
	} else {
		file, home, err = fileByPath(ctx, reg, tgt.loc, tgt.rel)
	}
	if err != nil {
		return err
	}

	resolver := &fed.Resolver{
		Roster: roster,
		Q:      fed.NewBackendQuerier(backendForRegistry(reg)),
		Probe:  memberProbe(reg),
	}
	res, err := resolver.Resolve(ctx, file.ID, home)
	if err != nil {
		if errors.Is(err, wal.ErrChainBroken) {
			return tserr.FedChainBrokenErr(home, err) // exit 6
		}
		return err
	}
	warn, oerr := resolveOutcome(res, file.ID)
	if oerr != nil {
		return oerr // PartialView → TV-FED exit 6; Missing → TV-OBJ exit 5
	}
	// Prefer the resolver's winning view: it names the authoritative current home.
	if res.View.Found {
		file = res.View.File
		home = res.View.Member
	}

	dest := fl.out
	if dest == "" {
		dest = filepath.Base(file.Path)
	}
	// Cheap local conflict guard (the vault-side name machinery lives in put).
	if !fl.force {
		if _, err := os.Stat(dest); err == nil {
			return tserr.ConfigErr("refusing to overwrite existing file "+dest+" (pass --force)", nil)
		}
	}

	m, ok := roster.Find(home)
	if !ok {
		return tserr.FedPartialViewErr(identity.Short(file.ID), []string{home}, nil)
	}
	b, err := backendForRegistry(reg)(m)
	if err != nil {
		return err
	}

	got, err := downloadVerified(ctx, b, file, dest)
	if err != nil {
		return err
	}

	freshness := freshnessNote(file, got)

	receiptPath := ""
	if !fl.noReceipt {
		receiptPath, err = writePullReceipt(file, home, got)
		if err != nil {
			// The bytes are already delivered (rename succeeded); the receipt is an
			// advisory recovery artifact (D24b), never authoritative. A failure to
			// write it must NOT fail the delivery — warn and continue (exit 0).
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: delivered %s but could not write pull receipt: %v\n", dest, err)
			receiptPath = ""
		}
	}

	out := getJSON{
		ID: file.ID, Short: identity.Short(file.ID), Path: home + "/" + file.Path, Home: home,
		Output: dest, SyncMode: file.SyncMode, Size: file.Size, SHA256: got,
		Freshness: freshness, Receipt: receiptPath,
		Answered: res.Reach.Answered, Unreachable: res.Reach.Unreachable, MovedHome: warn,
	}
	if fl.json {
		bj, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(bj))
	} else {
		printGetText(cmd, out, fl.noReceipt)
	}
	if warn {
		fmt.Fprintln(cmd.ErrOrStderr(), healWarning(file.ID))
	}
	return nil
}

// downloadVerified streams objects/<sha> into a temp file in dest's directory,
// hashing the bytes as they flow (one pass, constant memory), then verifies the
// digest before atomically renaming the temp into place. A git-mode digest
// mismatch is a hard integrity failure (TV-OBJ, exit 5): the temp is removed and
// nothing lands at dest — a corrupt download must never be delivered
// (never-silent-success). A manual-mode mismatch is NOT an error here (the bytes
// are the truth the node holds; freshness is reported by the caller). Returns the
// digest of the delivered bytes.
func downloadVerified(ctx context.Context, b backend.Backend, file catalog.File, dest string) (string, error) {
	dir := filepath.Dir(dest)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, ".tv-get-*.tmp")
	if err != nil {
		return "", tserr.ConfigErr("create temp file in "+dir, err)
	}
	tmpName := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	mw := io.MultiWriter(tmp, h)
	if err := b.Get(ctx, "objects/"+file.SHA256, mw); err != nil {
		_ = tmp.Close()
		return "", err // backend already yields TV-OBJ-01 for a missing blob
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", tserr.ConfigErr("fsync "+tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return "", tserr.ConfigErr("close "+tmpName, err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != file.SHA256 && file.SyncMode != catalog.SyncModeManual {
		// Every mode EXCEPT manual is content-addressed (the sha is the key), so a
		// mismatch is corruption — hard-fail. sync_mode is an open enum (D15): an
		// unknown future/skewed value is treated content-addressed (fail-CLOSED),
		// never delivered-and-labelled-"verified" (never-silent-success). Only
		// manual is editable in place (H12), where drift is legitimate.
		return "", tserr.ObjMissingErr(identity.Short(file.ID)+" (integrity: downloaded sha "+shortHash(got)+" != recorded "+shortHash(file.SHA256)+")", nil)
	}

	if err := os.Rename(tmpName, dest); err != nil {
		return "", tserr.ConfigErr("rename "+tmpName+" -> "+dest, err)
	}
	removeTmp = false
	if d, derr := os.Open(dir); derr == nil { // dir fsync is best-effort
		_ = d.Sync()
		_ = d.Close()
	}
	return got, nil
}

// freshnessNote describes the delivered bytes relative to the catalog's recorded
// sha. Non-manual modes are content-addressed and already integrity-verified by
// downloadVerified (a mismatch hard-failed before reaching here), so "verified"
// is truthful. manual-mode files are editable in place (H12), so a mismatch is
// legitimate drift, reported against last_scanned — never called "corrupt".
func freshnessNote(file catalog.File, got string) string {
	if file.SyncMode != catalog.SyncModeManual {
		return "verified"
	}
	ls := rfc(file.LastScanned)
	if got == file.SHA256 {
		return "verified against scan of " + ls
	}
	return "content has changed since last scan " + ls + " — run `tailvault vault scan` on the node"
}

// writePullReceipt records this download as an off-node identity backup (D24b):
// the file's full genesis record (self-certifying, id = sha256(genesis)) plus
// retrieval metadata. Written atomically and idempotently overwritten on
// re-download. Returns the receipt's path.
func writePullReceipt(file catalog.File, home, sha string) (string, error) {
	dir, err := identity.DefaultReceiptDir()
	if err != nil {
		return "", tserr.ConfigErr("locate receipts dir", err)
	}
	rec := identity.Receipt{
		ID: file.ID,
		Genesis: identity.Genesis{
			ContentSHA256: file.Genesis.ContentSHA256,
			OriginalPath:  file.Genesis.OriginalPath,
			IngestOpID:    file.Genesis.IngestOpID,
			OriginNode:    file.Genesis.OriginNode,
		},
		Path:         home + "/" + file.Path,
		SHA256AtPull: sha,
		PulledAt:     time.Now().UTC(),
		SourceNode:   home,
	}
	if err := identity.WriteReceipt(dir, rec); err != nil {
		return "", tserr.ConfigErr("write pull receipt", err)
	}
	return filepath.Join(dir, rec.ID+".toml"), nil
}

func printGetText(cmd *cobra.Command, g getJSON, noReceipt bool) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "got        %s\n", g.Path)
	fmt.Fprintf(w, "id         %s\n", g.Short)
	fmt.Fprintf(w, "home       %s\n", g.Home)
	fmt.Fprintf(w, "output     %s\n", g.Output)
	fmt.Fprintf(w, "size       %s\n", humanBytes(g.Size))
	fmt.Fprintf(w, "sha256     %s\n", shortHash(g.SHA256))
	fmt.Fprintf(w, "freshness  %s\n", g.Freshness)
	if noReceipt {
		fmt.Fprintln(w, "receipt    skipped (--no-receipt)")
	} else {
		fmt.Fprintf(w, "receipt    %s\n", g.Receipt)
	}
}
