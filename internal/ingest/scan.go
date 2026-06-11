package ingest

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// ChangeKind classifies a disk↔catalog difference found by Diff.
type ChangeKind int

const (
	Added   ChangeKind = iota // on disk, not in catalog → catch-up ingest
	Edited                    // hash drift with mtime/size moved since last_scanned
	Suspect                   // hash drift but mtime+size UNCHANGED → possible corruption; report only
	Moved                     // same content hash: old path gone, new path present
	Deleted                   // in catalog, not on disk
)

func (k ChangeKind) String() string {
	switch k {
	case Added:
		return "added"
	case Edited:
		return "edited"
	case Suspect:
		return "suspect"
	case Moved:
		return "moved"
	case Deleted:
		return "deleted"
	default:
		return "unknown"
	}
}

// Change is one reconciled difference.
type Change struct {
	Kind    ChangeKind
	Path    string       // current/new path
	OldPath string       // Moved only
	File    catalog.File // existing catalog entry (zero for Added)
	SHA256  string       // disk hash where computed
	Size    int64
}

// hashFileFunc is the hashing seam (overridden in tests to count invocations and
// assert lazy hashing).
var hashFileFunc = func(root, rel string) (string, error) {
	return hashFile(filepath.Join(root, filepath.FromSlash(rel)))
}

// Diff walks root (reusing BuildPlan + Ignore), compares against cat, and
// classifies changes. Hashing is lazy: an existing manual entry is re-hashed only
// when its mtime/size moved since last_scanned (or, with paranoid, always).
// sync_mode != "git" entries only — the git flow owns git entries' lifecycle.
func Diff(ctx context.Context, root string, ig *Ignore, cat *catalog.Catalog, paranoid bool, p Progress) ([]Change, error) {
	plan, err := BuildPlan(root, ig, nil)
	if err != nil {
		return nil, err
	}
	disk := make(map[string]Candidate, len(plan.Files))
	for _, c := range plan.Files {
		disk[c.Rel] = c
	}
	inCatalog := make(map[string]bool)

	var changes []Change
	var deleted []Change
	added := map[string]Candidate{}

	// Existing catalog entries: edited / suspect / deleted.
	for _, f := range cat.Files {
		if f.SyncMode == catalog.SyncModeGit {
			inCatalog[f.Path] = true
			continue // git flow owns these; scan does not reconcile them
		}
		inCatalog[f.Path] = true
		c, ok := disk[f.Path]
		if !ok {
			deleted = append(deleted, Change{Kind: Deleted, Path: f.Path, File: f, SHA256: f.SHA256})
			continue
		}
		moved := c.Size != f.Size || c.ModTime.After(f.LastScanned)
		if !paranoid && !moved {
			continue // fresh — skip hashing
		}
		sum, err := hashFileFunc(root, f.Path)
		if err != nil {
			return nil, err
		}
		if sum == f.SHA256 {
			continue // content unchanged (perhaps only mtime touched)
		}
		kind := Suspect
		if moved {
			kind = Edited
		}
		changes = append(changes, Change{Kind: kind, Path: f.Path, File: f, SHA256: sum, Size: c.Size})
	}

	// Disk files absent from the catalog: candidate adds (hash for genesis +
	// move-pairing).
	for rel, c := range disk {
		if inCatalog[rel] {
			continue
		}
		sum, err := hashFileFunc(root, rel)
		if err != nil {
			return nil, err
		}
		added[rel] = c
		changes = append(changes, Change{Kind: Added, Path: rel, SHA256: sum, Size: c.Size})
	}

	// Move pairing: a deleted entry and an added file with the SAME content hash
	// (uniquely) are one move (id preserved), not delete+ingest. Ambiguous
	// many-to-one matches fall back to delete+ingest (EDGE-CASES).
	changes = pairMoves(changes, &deleted)

	changes = append(changes, deleted...)
	return changes, nil
}

// pairMoves rewrites unique deleted↔added hash matches into Moved changes,
// removing the paired Added/Deleted. It mutates the deleted slice in place.
func pairMoves(changes []Change, deleted *[]Change) []Change {
	delByHash := map[string][]int{}
	for i, d := range *deleted {
		delByHash[d.SHA256] = append(delByHash[d.SHA256], i)
	}
	addByHash := map[string][]int{}
	for i, c := range changes {
		if c.Kind == Added {
			addByHash[c.SHA256] = append(addByHash[c.SHA256], i)
		}
	}
	removedAdds := map[int]bool{}
	removedDels := map[int]bool{}
	var moves []Change
	for h, dels := range delByHash {
		adds := addByHash[h]
		if len(dels) == 1 && len(adds) == 1 {
			d := (*deleted)[dels[0]]
			a := changes[adds[0]]
			moves = append(moves, Change{
				Kind: Moved, Path: a.Path, OldPath: d.Path, File: d.File, SHA256: h, Size: a.Size,
			})
			removedAdds[adds[0]] = true
			removedDels[dels[0]] = true
		}
		// else: ambiguous → leave as separate delete + ingest (EDGE-CASES).
	}
	// Drop paired adds from changes; drop paired dels from deleted.
	out := changes[:0]
	for i, c := range changes {
		if removedAdds[i] {
			continue
		}
		out = append(out, c)
	}
	newDel := (*deleted)[:0]
	for i, d := range *deleted {
		if removedDels[i] {
			continue
		}
		newDel = append(newDel, d)
	}
	*deleted = newDel
	return append(out, moves...)
}

// Apply emits catch-up WAL entries and updates the catalog for each change:
// Added → ingest (new genesis), Edited → scan entry + sha/last_scanned bump,
// Moved → move entry (id preserved), Deleted → delete entry. Suspect changes are
// NEVER applied (returned in skipped). A change whose blob has a pending intent
// fails the per-blob lock and is skipped; the rest still apply. now defaults to
// time.Now.
func Apply(ctx context.Context, log *wal.Log, cat *catalog.Catalog, catPath, node, actor string, changes []Change, now func() time.Time) (applied, skipped []Change, err error) {
	if now == nil {
		now = time.Now
	}
	var pendingDone []string

	for _, ch := range changes {
		if ch.Kind == Suspect {
			skipped = append(skipped, ch)
			continue
		}
		entry, mutate, ok := buildScanEntry(cat, node, actor, ch, now)
		if !ok {
			skipped = append(skipped, ch)
			continue
		}
		rec, aerr := log.AppendIntent(ctx, entry)
		if aerr != nil {
			if errors.Is(aerr, wal.ErrOpInFlight) {
				skipped = append(skipped, ch) // blob locked by another op; leave it
				continue
			}
			if !errors.Is(aerr, wal.ErrDuplicateOp) {
				return applied, skipped, aerr
			}
		}
		mutate()
		pendingDone = append(pendingDone, rec.Entry.OpID)
		applied = append(applied, ch)
	}

	if err := catalog.WriteAtomic(catPath, cat); err != nil {
		return applied, skipped, err
	}
	for _, id := range pendingDone {
		if err := log.MarkDone(ctx, id); err != nil {
			return applied, skipped, err
		}
	}
	return applied, skipped, nil
}

// buildScanEntry constructs the WAL entry and a deferred catalog mutation for a
// change. The mutation runs only after the intent is recorded (write-ahead
// ordering). ok=false for changes that cannot be turned into an entry.
func buildScanEntry(cat *catalog.Catalog, node, actor string, ch Change, now func() time.Time) (wal.Entry, func(), bool) {
	ts := now().UTC()
	switch ch.Kind {
	case Added:
		opID := wal.NewOpID()
		g := identity.Genesis{ContentSHA256: ch.SHA256, OriginalPath: ch.Path, IngestOpID: opID, OriginNode: node}
		id, err := identity.MintID(g)
		if err != nil {
			return wal.Entry{}, nil, false
		}
		entry := wal.Entry{
			OpID: opID, OpType: wal.OpIngest, BlobRefs: []string{id}, Actor: actor, CreatedAt: ts,
			Args: map[string]string{
				"path": ch.Path, "content_sha256": ch.SHA256, "origin_node": node,
				"sync_mode": catalog.SyncModeManual, "size": strconv.FormatInt(ch.Size, 10),
			},
		}
		return entry, func() {
			cat.Upsert(catalog.File{
				ID: id, Genesis: catalog.Genesis(g), SHA256: ch.SHA256, Path: ch.Path,
				SyncMode: catalog.SyncModeManual, Size: ch.Size,
				CreatedAt: ts, UpdatedAt: ts, LastScanned: ts,
			})
		}, true

	case Edited:
		entry := wal.Entry{
			OpID: wal.NewOpID(), OpType: wal.OpScan, BlobRefs: []string{ch.File.ID}, Actor: actor, CreatedAt: ts,
			Args: map[string]string{"path": ch.Path, "old_sha256": ch.File.SHA256, "new_sha256": ch.SHA256},
		}
		return entry, func() {
			f := ch.File
			f.SHA256 = ch.SHA256
			f.Size = ch.Size
			f.UpdatedAt = ts
			f.LastScanned = ts
			cat.Upsert(f)
		}, true

	case Moved:
		entry := wal.Entry{
			OpID: wal.NewOpID(), OpType: wal.OpMove, BlobRefs: []string{ch.File.ID}, Actor: actor, CreatedAt: ts,
			Args: map[string]string{"from": ch.OldPath, "to": ch.Path},
		}
		return entry, func() {
			f := ch.File // id + genesis preserved (dual addressing)
			cat.Remove(ch.OldPath)
			f.Path = ch.Path
			f.UpdatedAt = ts
			f.LastScanned = ts
			cat.Upsert(f)
		}, true

	case Deleted:
		entry := wal.Entry{
			OpID: wal.NewOpID(), OpType: wal.OpDelete, BlobRefs: []string{ch.File.ID}, Actor: actor, CreatedAt: ts,
			Args: map[string]string{"path": ch.Path},
		}
		return entry, func() { cat.Remove(ch.Path) }, true

	default:
		return wal.Entry{}, nil, false
	}
}
