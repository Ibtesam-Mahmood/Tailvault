package ingest

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// ErrPathNotPresent is returned by Track for a path that is not on the vault's
// disk: `track` registers reality, it never creates it. The command boundary
// maps it to a clean config-style error.
var ErrPathNotPresent = errors.New("ingest: path not present on the vault disk")

// TrackStatus is the per-path outcome of Track.
type TrackStatus int

const (
	StatusTracked TrackStatus = iota // newly registered (catch-up ingest)
	StatusAlready                    // already in the catalog, content unchanged — no-op
	StatusDrifted                    // already in the catalog but on-disk sha differs (run scan)
)

func (s TrackStatus) String() string {
	switch s {
	case StatusTracked:
		return "tracked"
	case StatusAlready:
		return "already-tracked"
	case StatusDrifted:
		return "drifted"
	default:
		return "unknown"
	}
}

// TrackResult reports what happened to one path.
type TrackResult struct {
	Path   string
	Status TrackStatus
	ID     string // full 64-hex file id (command shortens for display)
}

// TrackOpts bundles the backend-side dependencies. Everything goes through the
// Backend, so Track works identically on a local/taildrive mount and over SSH
// (hashing happens on the node via HashObject — a hand-dropped multi-GB file is
// never pulled across the tailnet just to register it).
type TrackOpts struct {
	Backend backend.Backend
	Log     *wal.Log
	Cat     *catalog.Catalog
	Node    string
	Actor   string
	Now     func() time.Time // defaults to time.Now
}

// Track registers each rel path (vault-relative, slash-separated) with the
// vault as ingestion path 1 (D18.1). A path already in the catalog is an
// idempotent no-op — StatusAlready, or StatusDrifted if its on-disk sha differs
// (track never re-hashes an existing entry into the catalog; that is scan's
// contract, task-34). A new path becomes a first-class manual file via a
// catch-up ingest: hash on the node → mint genesis/id → WAL intent → catalog
// upsert → persist catalog (PutOverwrite, atomic) → WAL done. Identity is minted
// deterministically from the path so a re-run never forks identity and an
// interrupted batch resumes cleanly. The Cat is mutated in place.
//
// Caller responsibilities: glob expansion and .tailvaultignore filtering (with
// the exact-path override, D22) happen at the command boundary; Track registers
// exactly the rels it is handed. Auth-gating the remote form is also the
// caller's job.
func Track(ctx context.Context, o TrackOpts, rels []string) ([]TrackResult, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	out := make([]TrackResult, 0, len(rels))

	for _, rel := range rels {
		if existing, ok := o.Cat.Find(rel); ok {
			// Idempotent: already tracked. Note drift but do NOT absorb it (scan's job).
			sha, err := o.Backend.HashObject(ctx, rel)
			if err != nil {
				if errors.Is(err, backend.ErrNotExist) {
					return out, ErrPathNotPresent
				}
				return out, err
			}
			st := StatusAlready
			if sha != existing.SHA256 {
				st = StatusDrifted
			}
			out = append(out, TrackResult{Path: rel, Status: st, ID: existing.ID})
			continue
		}

		m, err := o.Backend.Stat(ctx, rel)
		if err != nil {
			return out, err
		}
		if !m.Exists {
			return out, ErrPathNotPresent
		}
		sha, err := o.Backend.HashObject(ctx, rel)
		if err != nil {
			if errors.Is(err, backend.ErrNotExist) {
				return out, ErrPathNotPresent
			}
			return out, err
		}

		opID := ingestOpID(rel)
		g := identity.Genesis{ContentSHA256: sha, OriginalPath: rel, IngestOpID: opID, OriginNode: o.Node}
		id, err := identity.MintID(g)
		if err != nil {
			return out, err
		}
		entry := wal.Entry{
			OpID: opID, OpType: wal.OpIngest, BlobRefs: []string{id}, Actor: o.Actor, CreatedAt: now().UTC(),
			Args: map[string]string{
				"path": rel, "content_sha256": sha, "origin_node": o.Node,
				"sync_mode": catalog.SyncModeManual, "size": strconv.FormatInt(m.Size, 10),
			},
		}
		rec, err := o.Log.AppendIntent(ctx, entry)
		if err != nil && !errors.Is(err, wal.ErrDuplicateOp) {
			return out, err
		}
		row, err := rowFromEntry(rec.Entry, o.Node)
		if err != nil {
			return out, err
		}
		o.Cat.Upsert(row)
		// Persist the catalog over the backend (atomic overwrite) BEFORE the done
		// marker — write-ahead ordering. PutOverwrite works on local + SSH.
		if err := persistCatalog(ctx, o.Backend, o.Cat); err != nil {
			return out, err
		}
		if err := o.Log.MarkDone(ctx, opID); err != nil {
			return out, err
		}
		out = append(out, TrackResult{Path: rel, Status: StatusTracked, ID: id})
	}
	return out, nil
}

// persistCatalog atomically overwrites the node's catalog over the backend.
func persistCatalog(ctx context.Context, be backend.Backend, c *catalog.Catalog) error {
	bs, err := catalog.Encode(c)
	if err != nil {
		return err
	}
	return be.PutOverwrite(ctx, "meta/catalog.toml", bytes.NewReader(bs))
}
