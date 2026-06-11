package ingest

import (
	"context"
	"errors"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// Restore-identity sentinels (task-48). The command boundary maps them to tserr.
var (
	ErrNoTarget        = errors.New("ingest: no catalog entry at the target path")
	ErrAlreadyRestored = errors.New("ingest: target already carries the original id")
	ErrIDCollision     = errors.New("ingest: original id is already live on another entry")
)

// RestoreOpts bundles the backend-side deps for RestoreIdentity.
type RestoreOpts struct {
	Backend backend.Backend
	Log     *wal.Log
	Cat     *catalog.Catalog
	Node    string
	Actor   string
	Now     func() time.Time
}

// RestoreIdentity re-seeds the catalog entry at currentPath with the ORIGINAL,
// self-certifying id + genesis record — keeping the entry's current
// sha/path/sync_mode/timestamps — through a WAL-audited `restore` op. This is the
// deliberate, never-implicit identity resurrection of D24: a catalog rebuilt
// after disk loss regains the ids that locks/receipts still point at.
//
// The caller MUST have verified that g self-certifies originalID (VerifyID); this
// re-checks defensively. Errors: ErrNoTarget (no entry at currentPath),
// ErrAlreadyRestored (entry already carries originalID — no-op), ErrIDCollision
// (originalID already live on a DIFFERENT entry in THIS catalog — restoring would
// create two live claims to one identity; a federation-wide fan-out collision
// check (task-32) is the command's broader guard). Cat is mutated in place.
func RestoreIdentity(ctx context.Context, o RestoreOpts, currentPath, originalID string, g identity.Genesis) (catalog.File, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	// Defensive self-certification: the record must hash to the claimed id.
	got, err := identity.VerifyID(g)
	if err != nil {
		return catalog.File{}, err
	}
	if got != originalID {
		return catalog.File{}, errors.New("ingest: genesis record does not self-certify the original id")
	}

	f, ok := o.Cat.Find(currentPath)
	if !ok {
		return catalog.File{}, ErrNoTarget
	}
	if f.ID == originalID {
		return catalog.File{}, ErrAlreadyRestored
	}
	if other, ok := o.Cat.FindID(originalID); ok && other.Path != currentPath {
		return catalog.File{}, ErrIDCollision
	}

	opID := wal.NewOpID()
	// The op record must be PROJECTION-SUFFICIENT (SPEC §9c: catalog = projection of
	// the WAL; fix-35-B). restored_id = sha256(g) is one-way, so recording only the
	// id leaves a rebuild unable to reconstruct f.Genesis. Carry the full genesis
	// PREIMAGE (4 small, non-secret fields) in the args so ProjectCatalog's OpRestore
	// case (fix-35-A) can rebuild ID+Genesis and self-certify hash(genesis)==
	// restored_id. Key names mirror the OpIngest entry (content_sha256/origin_node),
	// adding original_path + ingest_op_id explicitly — for a restore the genesis's
	// birth path and ingest op id are the ORIGINAL file's, not this op's own
	// path/OpID (so, unlike OpIngest, they cannot be derived and must be recorded).
	entry := wal.Entry{
		OpID: opID, OpType: wal.OpRestore, BlobRefs: []string{originalID}, Actor: o.Actor, CreatedAt: now().UTC(),
		Args: map[string]string{
			"path": currentPath, "old_id": f.ID, "restored_id": originalID,
			"content_sha256": g.ContentSHA256, "original_path": g.OriginalPath,
			"ingest_op_id": g.IngestOpID, "origin_node": g.OriginNode,
		},
	}
	if _, err := o.Log.AppendIntent(ctx, entry); err != nil && !errors.Is(err, wal.ErrDuplicateOp) {
		return catalog.File{}, err
	}

	// Swap identity to the original; keep current content/path/sync_mode.
	f.ID = originalID
	f.Genesis = catalog.Genesis(g)
	f.UpdatedAt = now().UTC()
	o.Cat.Upsert(f)

	if err := persistCatalog(ctx, o.Backend, o.Cat); err != nil {
		return catalog.File{}, err
	}
	if err := o.Log.MarkDone(ctx, opID); err != nil {
		return catalog.File{}, err
	}
	return f, nil
}
