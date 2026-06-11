package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
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
// the file list, then replays every DONE op in seq order through the SAME applyOp
// core used by forward replay — so a rebuild can never diverge from how ops were
// originally applied. Intent/failed ops are SKIPPED (only completed effects are
// real). Timestamps come from each entry's immutable CreatedAt, so two rebuilds
// of the same WAL produce byte-identical catalogs (deterministic; mirrors
// bootstrap's resume guarantee). recs MUST be the chain-verified output of
// wal.Log.Read (Read verifies the hash chain before returning); ProjectCatalog
// does not re-verify it.
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
		if err := applyOp(rebuilt, rec.Entry, node, rec.Entry.CreatedAt.UTC()); err != nil {
			return nil, err
		}
	}
	rebuilt.Canonicalize()
	return rebuilt, nil
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
