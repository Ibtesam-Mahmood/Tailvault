package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// reservedNames are vault-root entries never ingested: the meta area (catalog,
// WAL, auth), the git-flow storage areas, and the ignore file itself.
var reservedNames = map[string]bool{
	"meta":         true,
	"objects":      true,
	"refs":         true,
	IgnoreFileName: true,
}

// Candidate is one file selected for ingestion.
type Candidate struct {
	Rel     string // vault-relative, slash-separated path
	Size    int64
	ModTime time.Time // for vault scan freshness (Task 34)
}

// Plan is the candidate set after walk + ignore + deselect.
type Plan struct {
	Root    string
	Files   []Candidate // sorted by Rel byte-wise ascending
	Ignored []string    // ignored relative paths (for --dry-run reporting), sorted
}

// BuildPlan walks root, applying reserved-name exclusions and the
// .tailvaultignore. Symlinks are skipped with no error (cycle/escape safety —
// EDGE-CASES); the caller may surface a warning. explicit is the set of paths an
// explicit `track` forces in regardless of ignore (D22); pass nil for bootstrap.
func BuildPlan(root string, ig *Ignore, explicit map[string]bool) (Plan, error) {
	if ig == nil {
		ig = &Ignore{}
	}
	p := Plan{Root: root}
	err := filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if abs == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, abs)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		top := rel
		if i := indexSlash(rel); i >= 0 {
			top = rel[:i]
		}
		if reservedNames[top] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// Skip symlinks entirely (don't follow; don't ingest) for cycle/escape
		// safety.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if ig.Match(rel, explicit) {
				return fs.SkipDir
			}
			return nil
		}
		if ig.Match(rel, explicit) {
			p.Ignored = append(p.Ignored, rel)
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		p.Files = append(p.Files, Candidate{Rel: rel, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return Plan{}, err
	}
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Rel < p.Files[j].Rel })
	sort.Strings(p.Ignored)
	return p, nil
}

func indexSlash(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// Progress receives per-file events for the CLI's progress UX.
type Progress func(done, total int, doneBytes, totalBytes int64, current string)

// BootstrapOpts bundles the bootstrap inputs. (An options struct is used rather
// than a long positional signature: bootstrap needs the origin node + actor for
// WAL/genesis, an injectable clock for deterministic timestamps — load-bearing
// for the byte-identical-resume guarantee — and a catalog-flush cadence.)
type BootstrapOpts struct {
	Root     string
	Node     string // origin node (genesis + catalog)
	Actor    string // WAL actor (whois/git identity)
	Log      *wal.Log
	Cat      *catalog.Catalog
	CatPath  string
	Plan     Plan
	Progress Progress         // optional
	Now      func() time.Time // optional; defaults to time.Now
	BatchN   int              // catalog flush cadence; default 256
}

// Bootstrap ingests the plan. For each candidate, the file ID (and so its WAL
// op id) is DERIVED DETERMINISTICALLY from the vault-relative path, which is what
// makes the operation idempotent and resumable to a byte-identical catalog: a
// re-run recomputes the same ids, replays already-recorded WAL entries into the
// catalog, re-executes pending ones, and ingests only the genuinely new files.
//
// Per-file ordering (bootstrap variant — the blob bytes already exist on disk):
// hash → WAL intent (carrying the genesis fields) → catalog upsert → WAL done.
// Catalog flushes are batched (the WAL, not the catalog, is the recovery record);
// done markers for a batch are written only AFTER that batch's catalog flush, so
// the write-ahead ordering (catalog before done) holds.
func Bootstrap(ctx context.Context, o BootstrapOpts) error {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	batchN := o.BatchN
	if batchN <= 0 {
		batchN = 256
	}

	existing, err := o.Log.Read(ctx)
	if err != nil {
		return err
	}
	byOpID := make(map[string]wal.Rec, len(existing))
	for _, r := range existing {
		byOpID[r.Entry.OpID] = r
	}

	total := len(o.Plan.Files)
	var totalBytes int64
	for _, c := range o.Plan.Files {
		totalBytes += c.Size
	}

	var doneBytes int64
	var pendingDone []string // op ids whose .done marker is owed after the next flush

	flush := func() error {
		if err := catalog.WriteAtomic(o.CatPath, o.Cat); err != nil {
			return err
		}
		for _, id := range pendingDone {
			if err := o.Log.MarkDone(ctx, id); err != nil {
				return err
			}
		}
		pendingDone = pendingDone[:0]
		return nil
	}

	for i, c := range o.Plan.Files {
		opID := ingestOpID(c.Rel)

		if rec, ok := byOpID[opID]; ok {
			// Already recorded: replay it into the catalog (idempotent). If it was
			// never completed, owe a done marker after the flush.
			row, rerr := rowFromEntry(rec.Entry, o.Node)
			if rerr != nil {
				return rerr
			}
			o.Cat.Upsert(row)
			if rec.State != wal.StateDone {
				pendingDone = append(pendingDone, opID)
			}
		} else {
			sum, err := hashFile(filepath.Join(o.Root, filepath.FromSlash(c.Rel)))
			if err != nil {
				return err
			}
			g := identity.Genesis{
				ContentSHA256: sum,
				OriginalPath:  c.Rel,
				IngestOpID:    opID,
				OriginNode:    o.Node,
			}
			id, err := identity.MintID(g)
			if err != nil {
				return err
			}
			entry := wal.Entry{
				OpID:      opID,
				OpType:    wal.OpIngest,
				BlobRefs:  []string{id},
				Actor:     o.Actor,
				CreatedAt: now().UTC(),
				Args: map[string]string{
					"path":           c.Rel,
					"content_sha256": sum,
					"origin_node":    o.Node,
					"sync_mode":      catalog.SyncModeManual,
					"size":           strconv.FormatInt(c.Size, 10),
				},
			}
			rec, err := o.Log.AppendIntent(ctx, entry)
			if err != nil && !errors.Is(err, wal.ErrDuplicateOp) {
				return err
			}
			row, rerr := rowFromEntry(rec.Entry, o.Node)
			if rerr != nil {
				return rerr
			}
			o.Cat.Upsert(row)
			pendingDone = append(pendingDone, opID)
		}

		doneBytes += c.Size
		if o.Progress != nil {
			o.Progress(i+1, total, doneBytes, totalBytes, c.Rel)
		}
		if len(pendingDone) >= batchN {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

// rowFromEntry reconstructs a catalog row from an ingest WAL entry. Because the
// entry is immutable and carries all genesis/content fields, two runs produce an
// identical row (timestamps come from the entry's CreatedAt, not wall-clock at
// reconstruction) — the basis of byte-identical resume.
func rowFromEntry(e wal.Entry, node string) (catalog.File, error) {
	g, err := identity.FromIngestEntry(e, node)
	if err != nil {
		return catalog.File{}, err
	}
	id, err := identity.MintID(g)
	if err != nil {
		return catalog.File{}, err
	}
	var size int64
	if s := e.Args["size"]; s != "" {
		size, _ = strconv.ParseInt(s, 10, 64)
	}
	sync := e.Args["sync_mode"]
	if sync == "" {
		sync = catalog.SyncModeManual
	}
	ts := e.CreatedAt.UTC()
	return catalog.File{
		ID: id,
		Genesis: catalog.Genesis{
			ContentSHA256: g.ContentSHA256,
			OriginalPath:  g.OriginalPath,
			IngestOpID:    g.IngestOpID,
			OriginNode:    g.OriginNode,
		},
		SHA256:      e.Args["content_sha256"],
		Path:        e.Args["path"],
		SyncMode:    sync,
		Size:        size,
		CreatedAt:   ts,
		UpdatedAt:   ts,
		LastScanned: ts,
	}, nil
}

// ingestOpID derives a deterministic, UUIDv4-formatted op id from the
// vault-relative path. Determinism (not randomness) is required so an
// interrupted bootstrap re-mints identical file ids and converges to a
// byte-identical catalog; the version/variant bits are set so the id is
// format-compatible with §10's UUIDv4 op ids.
func ingestOpID(rel string) string {
	sum := sha256.Sum256([]byte("tailvault/bootstrap-ingest\x00" + rel))
	var b [16]byte
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return hex.EncodeToString(b[:])
}

// hashFile streams a file through sha256 (never reads it fully into memory).
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
