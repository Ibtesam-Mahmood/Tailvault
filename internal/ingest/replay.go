package ingest

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// ReplayOp idempotently COMPLETES a pending catch-up op emitted by bootstrap
// (task-33) or scan (task-34) — op types ingest | scan | move | delete. It is the
// seam `ops retry` (task-37) calls so retry reuses this package's write-ahead
// ordering instead of duplicating it: the intent already exists, so ReplayOp
// performs only the remaining steps — apply the op's catalog mutation
// (reconstructed from the IMMUTABLE WAL entry), persist the catalog atomically
// (catalog.WriteAtomic = temp+fsync+rename), then mark the op done.
//
// It is safe to call on an already-completed op (catalog Upsert/Remove are
// idempotent; MarkDone of a done op is a no-op). On ANY error the op is left
// pending (no done marker) so the caller can MarkFailed and a later retry can
// re-run it. now defaults to time.Now.
//
// It does NOT re-verify blob bytes or the WAL chain (Read already verifies the
// chain before a Rec is handed out; byte integrity is verify's job, task-38).
// gc/roster/sync_mode ops are NOT handled here — they belong to their own
// packages' executors.
func ReplayOp(ctx context.Context, log *wal.Log, cat *catalog.Catalog, catPath, node string, rec wal.Rec, now func() time.Time) error {
	if now == nil {
		now = time.Now
	}
	ts := now().UTC()
	if err := applyOp(cat, rec.Entry, node, ts); err != nil {
		return err
	}
	if err := catalog.WriteAtomic(catPath, cat); err != nil {
		return err
	}
	return log.MarkDone(ctx, rec.Entry.OpID)
}

// applyOp mutates cat in place to reflect one WAL entry's effect, reconstructed
// purely from the IMMUTABLE entry (no blob/disk access). It is the single shared
// catalog-mutation core for both forward replay (ReplayOp) and full projection
// (ProjectCatalog). ts supplies UpdatedAt/LastScanned for the scan/move ops that
// record a freshness bump — ReplayOp passes wall-clock now; ProjectCatalog passes
// the entry's own CreatedAt so a rebuild is byte-deterministic. ingest rows take
// their timestamps from the entry itself (rowFromEntry), so they are deterministic
// either way. It does NOT persist or mark the op — the caller owns that.
func applyOp(cat *catalog.Catalog, e wal.Entry, node string, ts time.Time) error {
	switch e.OpType {
	case wal.OpIngest:
		row, err := rowFromEntry(e, node)
		if err != nil {
			return fmt.Errorf("ingest: replay ingest %s: %w", e.OpID, err)
		}
		cat.Upsert(row)

	case wal.OpScan: // a manual-edit catch-up: bump the current sha + freshness
		path := e.Args["path"]
		f, ok := cat.Find(path)
		if !ok {
			return fmt.Errorf("ingest: replay scan %s: no catalog entry for %q", e.OpID, path)
		}
		if s := e.Args["new_sha256"]; s != "" {
			f.SHA256 = s
		}
		f.UpdatedAt = ts
		f.LastScanned = ts
		cat.Upsert(f)

	case wal.OpMove:
		from, to := e.Args["from"], e.Args["to"]
		f, ok := findMoved(cat, e.BlobRefs, from)
		if ok && f.Path != to {
			cat.Remove(f.Path)
			f.Path = to
			f.UpdatedAt = ts
			f.LastScanned = ts
			cat.Upsert(f)
		}
		// if !ok the move was already applied (or the entry is gone) — idempotent.

	case wal.OpDelete:
		cat.Remove(e.Args["path"])

	default:
		return fmt.Errorf("ingest: replay: unsupported op_type %q (op %s)", e.OpType, e.OpID)
	}
	return nil
}

// ProjectCatalog rebuilds a catalog as a pure PROJECTION of the WAL — the SG-6
// disaster-recovery story for a missing or torn meta/catalog.toml. The catalog
// has always been documented as "a projection of the WAL (the recovery record)"
// (task-33); this is that projection made explicit and reachable.
//
// It copies base's header (version, vault_name, node, federation roster), clears
// the file list, then replays every DONE op in seq order through projectOp.
// Unlike ReplayOp's applyOp (the catch-up subset: ingest/scan/move/delete, which
// HARD-ERRORS on anything else), projectOp tolerates the FULL per-node WAL
// vocabulary: a real vault's WAL routinely carries gc, restore, cross-member
// move-forwarding, and sync_mode ops. It PROJECTS every catalog-file-mutating op
// and SKIPS ops with no file-list effect (roster, passwd) and unknown/future op
// types — it must NEVER error on an op type, or a rebuild would brick on any vault
// that has merely run gc (auto-delete is on by default).
//
// Intent/failed ops are SKIPPED (only completed effects are real). Timestamps come
// from each entry's immutable CreatedAt, so two rebuilds of the same WAL produce
// byte-identical catalogs (deterministic). recs MUST be the chain-verified output
// of wal.Log.Read; ProjectCatalog does not re-verify the chain.
//
// It returns the rebuilt catalog Canonicalized and ready to persist; the CALLER
// owns writing it (locally via catalog.WriteAtomic, or over a backend via
// backend.PutOverwrite — there is no new WAL op, the WAL is the source being
// replayed, not appended to).
func ProjectCatalog(base *catalog.Catalog, recs []wal.Rec, node string) (*catalog.Catalog, error) {
	rebuilt := &catalog.Catalog{
		Version:    base.Version,
		VaultName:  base.VaultName,
		Node:       base.Node,
		Federation: base.Federation,
		Files:      nil,
	}
	for _, rec := range recs {
		if rec.State != wal.StateDone {
			continue // only completed effects are real; intent/failed never applied
		}
		projectOp(rebuilt, rec.Entry, node, rec.Entry.CreatedAt.UTC())
	}
	rebuilt.Canonicalize()
	return rebuilt, nil
}

// projectOp applies one DONE WAL entry's catalog-file-list effect during a full
// projection. It handles every catalog-MUTATING op type and SKIPS the rest; it
// NEVER errors (forward-compatibility — a rebuild tolerates op types added by
// later blocks). It reads the canonical op arg keys the merged writers emit
// (restore.go / vault_mv.go / vault_syncmode.go). It is the projection
// counterpart to applyOp, which stays strict for ReplayOp's catch-up retries.
func projectOp(cat *catalog.Catalog, e wal.Entry, node string, ts time.Time) {
	switch e.OpType {
	case wal.OpIngest:
		if row, err := rowFromEntry(e, node); err == nil {
			cat.Upsert(row)
		}

	case wal.OpScan:
		if f, ok := cat.Find(e.Args["path"]); ok {
			if s := e.Args["new_sha256"]; s != "" {
				f.SHA256 = s
			}
			f.UpdatedAt = ts
			f.LastScanned = ts
			cat.Upsert(f)
		}

	case wal.OpMove:
		projectMove(cat, e, ts)

	case wal.OpDelete:
		cat.Remove(e.Args["path"])

	case wal.OpGC:
		// gc deleted the doomed blobs and removed their catalog entries; BlobRefs
		// carries the doomed FILE IDs. SKIPPING would resurrect entries pointing at
		// deleted blobs → integrity failure.
		for _, id := range e.BlobRefs {
			if f, ok := cat.FindID(id); ok {
				cat.Remove(f.Path)
			}
		}

	case wal.OpRestore:
		projectRestore(cat, e, ts)

	case wal.OpSyncMode:
		if f, ok := cat.Find(e.Args["path"]); ok {
			if m := e.Args["to_mode"]; m != "" {
				f.SyncMode = m
			}
			if s := e.Args["new_sha256"]; s != "" {
				f.SHA256 = s
			}
			if t, err := time.Parse(time.RFC3339Nano, e.Args["last_scanned"]); err == nil {
				f.LastScanned = t.UTC()
			}
			f.UpdatedAt = ts
			cat.Upsert(f)
		}

	case wal.OpRoster, wal.OpPasswd:
		// No catalog FILE-LIST effect (roster header comes from base; passwd touches
		// meta/auth). Skip.

	default:
		// Unknown/future op type: skip, never error.
	}
}

// projectMove applies a move op during projection — the three shapes vault mv /
// scan emit (SPEC v2 §10):
//   - cross-member SOURCE (args[moved_to] set): the file LEFT this node → remove
//     the entry (the forwarding pointer lives in the WAL, not the catalog);
//   - cross-member DEST (args[id]+args[dest_path], no moved_to): the file ARRIVED
//     → add the row, reconstructed from the op's genesis preimage (the source-
//     minted genesis is not otherwise in the dest's WAL). sync_mode/size are not
//     journaled on the dest move op, so a rebuilt moved-in row defaults sync_mode
//     to manual (conservative for gc) — a documented projection limitation;
//   - intra-member rename (args[from]/args[to] paths): rewrite the entry's path
//     in place (id/genesis preserved).
func projectMove(cat *catalog.Catalog, e wal.Entry, ts time.Time) {
	if e.Args["moved_to"] != "" { // cross-member source: file left this node
		for _, id := range e.BlobRefs {
			if f, ok := cat.FindID(id); ok {
				cat.Remove(f.Path)
			}
		}
		return
	}
	if dest := e.Args["dest_path"]; dest != "" && e.Args["id"] != "" { // cross-member dest: arrival
		cat.Upsert(catalog.File{
			ID:          e.Args["id"],
			Genesis:     genesisFromArgs(e),
			SHA256:      e.Args["content_sha256"],
			Path:        dest,
			SyncMode:    syncModeOrManual(e.Args["sync_mode"]),
			Size:        parseSize(e.Args["size"]),
			CreatedAt:   ts,
			UpdatedAt:   ts,
			LastScanned: ts,
		})
		return
	}
	// Intra-member rename.
	from, to := e.Args["from"], e.Args["to"]
	if to == "" {
		return
	}
	if f, ok := findMoved(cat, e.BlobRefs, from); ok && f.Path != to {
		cat.Remove(f.Path)
		f.Path = to
		f.UpdatedAt = ts
		f.LastScanned = ts
		cat.Upsert(f)
	}
}

// projectRestore re-applies a restore-identity op: swap the target entry's id to
// the restored id and repopulate its genesis from the preimage the op carries
// (canonical un-prefixed keys, restore.go). The target is the entry at
// args[path]; if it is absent (a later move/gc removed it) the op is a no-op. The
// preimage is verified to self-certify the restored id (a tamper guard) — a
// mismatch leaves the entry's identity untouched rather than corrupting it.
func projectRestore(cat *catalog.Catalog, e wal.Entry, ts time.Time) {
	path, restored := e.Args["path"], e.Args["restored_id"]
	if path == "" || restored == "" {
		return
	}
	f, ok := cat.Find(path)
	if !ok {
		return
	}
	g := genesisFromArgs(e)
	if ok, err := identity.Verify(toIdentityGenesis(g), restored); err != nil || !ok {
		return // preimage does not hash to restored_id — refuse to corrupt identity
	}
	f.ID = restored
	f.Genesis = g
	f.UpdatedAt = ts
	cat.Upsert(f)
}

// genesisFromArgs reconstructs a catalog.Genesis from the canonical un-prefixed
// genesis preimage args the restore / cross-member-move-dest writers emit
// (content_sha256 / original_path / ingest_op_id / origin_node — matching the
// OpIngest key names).
func genesisFromArgs(e wal.Entry) catalog.Genesis {
	return catalog.Genesis{
		ContentSHA256: e.Args["content_sha256"],
		OriginalPath:  e.Args["original_path"],
		IngestOpID:    e.Args["ingest_op_id"],
		OriginNode:    e.Args["origin_node"],
	}
}

func toIdentityGenesis(g catalog.Genesis) identity.Genesis {
	return identity.Genesis{
		ContentSHA256: g.ContentSHA256,
		OriginalPath:  g.OriginalPath,
		IngestOpID:    g.IngestOpID,
		OriginNode:    g.OriginNode,
	}
}

func syncModeOrManual(s string) string {
	if s == "" {
		return catalog.SyncModeManual
	}
	return s
}

func parseSize(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// findMoved locates the entry a move op refers to: by its stable file ID
// (blob_refs[0]) first, falling back to the source path.
func findMoved(cat *catalog.Catalog, blobRefs []string, from string) (catalog.File, bool) {
	if len(blobRefs) > 0 {
		if f, ok := cat.FindID(blobRefs[0]); ok {
			return f, true
		}
	}
	return cat.Find(from)
}
