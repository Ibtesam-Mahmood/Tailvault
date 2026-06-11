package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/ingest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// FindingKind classifies a 3-way (lock ↔ catalog ↔ disk) discrepancy.
type FindingKind int

const (
	OK               FindingKind = iota
	LockOnlyEntry                // in lock, absent from catalog
	CatalogOnlyEntry             // in catalog, not in lock (informational; manual file)
	FieldMismatch                // sha (and, with lock-v2, id/genesis) disagree lock↔catalog
	MissingOnDisk                // catalog entry, no file on disk
	EditedSinceScan              // manual file: hash drift + mtime/size moved → run scan
	Corrupt                      // bytes differ, freshness says no edit (or git-mode any drift)
	PendingOpState               // intermediate state explained by a pending WAL intent
	ChainBroken                  // WAL hash-chain verification failed
	GenesisInvalid               // sha256(genesis) != id
)

func (k FindingKind) String() string {
	switch k {
	case OK:
		return "ok"
	case LockOnlyEntry:
		return "lock-only"
	case CatalogOnlyEntry:
		return "catalog-only"
	case FieldMismatch:
		return "field-mismatch"
	case MissingOnDisk:
		return "missing-on-disk"
	case EditedSinceScan:
		return "edited-since-scan"
	case Corrupt:
		return "corrupt"
	case PendingOpState:
		return "pending-op"
	case ChainBroken:
		return "chain-broken"
	case GenesisInvalid:
		return "genesis-invalid"
	default:
		return "unknown"
	}
}

// ThreeFinding is one 3-way reconciliation result with a repair pointer.
type ThreeFinding struct {
	Kind   FindingKind
	Path   string
	ID     string // short form
	Detail string // diagnosis + repair pointer (heal / ops / scan / re-push)
}

// Options tunes ThreeWay.
type Options struct {
	Now func() time.Time // injectable clock (freshness boundary); defaults to time.Now
	// SkipDisk omits the catalog↔disk manual-file check (the only step needing a
	// locally-accessible root + mtime). Set for a remote (SSH) vault, where the
	// disk check is deferred like scan/bootstrap (DG-33.1); the WAL spot-check,
	// genesis self-cert, and lock↔catalog reconciliation still run.
	SkipDisk bool
}

// ThreeWay reconciles lock ↔ catalog ↔ disk for a locally-accessible vault root
// and spot-checks the WAL chain. READ-ONLY: it never repairs, only diagnoses and
// points. lk may be nil (manual-only vault → catalog↔disk + WAL only); cat may be
// nil (non-federated vault → no 3-way findings; the caller's v1 Run covers
// objects/). The manual-file disk check is local (os.Stat needs mtime, which the
// Backend interface does not expose — consistent with scan/bootstrap's local-root
// model, DG-33.1); the WAL is read through log (any backend).
//
// Pending-op suppression runs BEFORE any corruption verdict (load-bearing):
// a file whose id is in a pending WAL intent gets one PendingOpState finding and
// no corruption verdict — half-executed ops are the WAL's job.
//
// NOTE (DG-38.1): the lock↔catalog cross-check compares sha (v1 lock fields) +
// presence; the id/genesis byte-equality + lock-side self-certification land with
// lock schema v2 (task-35), which embeds genesis records in lock entries.
func ThreeWay(ctx context.Context, root string, lk *lock.Lock, cat *catalog.Catalog, log *wal.Log, opt Options) ([]ThreeFinding, error) {
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	var fs []ThreeFinding

	// 1. WAL chain integrity. Read verifies every link by hashing the raw on-disk
	// bytes (not a cached value), which is itself the independent re-derivation;
	// a break is TV-FED-03 and leaves the pending set unknowable.
	pending := map[string]bool{} // file id → has an in-flight intent
	chainOK := true
	if log != nil {
		if _, err := log.Read(ctx); err != nil {
			if errors.Is(err, wal.ErrChainBroken) {
				chainOK = false
				fs = append(fs, ThreeFinding{Kind: ChainBroken,
					Detail: "WAL hash-chain verification failed (TV-FED-03) — restore the node's WAL from a clone/backup: " + err.Error()})
			} else {
				return nil, err
			}
		}
		if chainOK {
			pend, err := log.Pending(ctx, "")
			if err != nil {
				return nil, err
			}
			for _, r := range pend {
				for _, id := range r.Entry.BlobRefs {
					pending[id] = true
				}
			}
		}
	}

	if cat == nil {
		return fs, nil // non-federated: v1 Run covers objects/.
	}

	lockByPath := map[string]lock.Entry{}
	catByPath := map[string]bool{}
	if lk != nil {
		for _, e := range lk.Entries {
			lockByPath[e.Path] = e
		}
	}

	for _, f := range cat.Files {
		catByPath[f.Path] = true

		// Pending suppression FIRST — never call an in-flight op's state corruption.
		if pending[f.ID] {
			fs = append(fs, ThreeFinding{Kind: PendingOpState, Path: f.Path, ID: identity.Short(f.ID),
				Detail: "an op is in flight on this file — run `tailvault ops`; verify after it clears"})
			continue
		}

		// Genesis self-certification (catalog side): sha256(genesis) must equal id.
		if ok, _ := identity.Verify(identity.Genesis(f.Genesis), f.ID); !ok {
			fs = append(fs, ThreeFinding{Kind: GenesisInvalid, Path: f.Path, ID: identity.Short(f.ID),
				Detail: "catalog genesis record does not self-certify its id — possible tamper/corruption"})
			continue
		}

		// lock ↔ catalog (v1 fields). lock-v2 id/genesis cross-check: DG-38.1.
		if lk != nil {
			if e, ok := lockByPath[f.Path]; ok {
				if e.SHA256 != f.SHA256 {
					fs = append(fs, ThreeFinding{Kind: FieldMismatch, Path: f.Path, ID: identity.Short(f.ID),
						Detail: fmt.Sprintf("lock sha %s != catalog sha %s — run `tailvault heal`", short(e.SHA256), short(f.SHA256))})
				}
			} else if f.SyncMode != catalog.SyncModeGit {
				fs = append(fs, ThreeFinding{Kind: CatalogOnlyEntry, Path: f.Path, ID: identity.Short(f.ID),
					Detail: "manual file not referenced by the repo lock (informational)"})
			}
		}

		// catalog ↔ disk (manual files live at their logical path on the node;
		// git-mode bytes are content-addressed under objects/ and are the v1 Run's
		// responsibility — skipped here). Skipped entirely for a remote vault.
		if opt.SkipDisk || f.SyncMode == catalog.SyncModeGit {
			continue
		}
		size, mtime, sum, ok, err := localStatHash(filepath.Join(root, filepath.FromSlash(f.Path)))
		if err != nil {
			return nil, err
		}
		if !ok {
			fs = append(fs, ThreeFinding{Kind: MissingOnDisk, Path: f.Path, ID: identity.Short(f.ID),
				Detail: "catalog entry has no file on disk — re-push from a clone or run `tailvault vault scan`"})
			continue
		}
		if sum == f.SHA256 {
			continue // OK
		}
		// Drift: edited-vs-corrupt via the SHARED heuristic (ingest.ClassifyDrift)
		// so scan and verify can never disagree.
		switch ingest.ClassifyDrift(f, size, mtime) {
		case ingest.Edited:
			fs = append(fs, ThreeFinding{Kind: EditedSinceScan, Path: f.Path, ID: identity.Short(f.ID),
				Detail: "manual file edited since last scan — run `tailvault vault scan` to absorb"})
		default: // Suspect: mtime+size unchanged but bytes differ → rot
			fs = append(fs, ThreeFinding{Kind: Corrupt, Path: f.Path, ID: identity.Short(f.ID),
				Detail: "content differs from catalog sha with no edit signal — possible corruption"})
		}
	}

	// lock ↔ catalog: lock entries absent from the catalog.
	if lk != nil {
		for _, e := range lk.Entries {
			if !catByPath[e.Path] {
				fs = append(fs, ThreeFinding{Kind: LockOnlyEntry, Path: e.Path, ID: short(e.SHA256),
					Detail: "in the repo lock but not in the node catalog — re-push, or run `tailvault heal`"})
			}
		}
	}
	return fs, nil
}

// ExitCode maps findings to the bucketed process exit code (most severe wins):
// Corrupt/MissingOnDisk → 5, ChainBroken → 6, GenesisInvalid → 5, everything else
// (informational) → 0.
func ExitCode(fs []ThreeFinding) int {
	code := 0
	for _, f := range fs {
		switch f.Kind {
		case ChainBroken:
			if code < 6 {
				code = 6
			}
		case Corrupt, MissingOnDisk, GenesisInvalid, FieldMismatch, LockOnlyEntry:
			if code < 5 {
				code = 5
			}
		}
	}
	return code
}

// localStatHash stats + hashes a local file; ok=false (no error) when it is
// absent.
func localStatHash(path string) (size int64, mtime time.Time, sha string, ok bool, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, time.Time{}, "", false, nil
		}
		return 0, time.Time{}, "", false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, time.Time{}, "", false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return 0, time.Time{}, "", false, err
	}
	return fi.Size(), fi.ModTime(), hex.EncodeToString(h.Sum(nil)), true, nil
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}
