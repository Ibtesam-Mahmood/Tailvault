# Task 38: 3-Way Verify — lock ↔ catalog ↔ disk, Edited-vs-Corrupt & WAL Spot-Check

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Catalog (vault-side state) + atomicity standards" (3-way verify), "File identity" (edited-vs-corrupt); Part II task breakdown → 3.12 · **Block:** 3 — Vault catalog + federation core · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Task 23 (`verify` v1 — re-hash stored blobs), Task 28 (`catalog`), Task 29 (`wal` — chain verify, pending detection), Task 34 (Edited/Suspect classification from `internal/ingest`) · **Type:** Implementation

## Summary

v1 `verify` re-hashes stored blobs against the lock and reports
corrupt/missing. Under federation there are now **three** sources of truth
that must agree — the repo-committed **lock**, the node's **catalog**, and
the **disk** bytes — and the frozen atomicity standard names 3-way verify as
the detector for crashes anywhere in the write-ahead ordering. This task
extends `verify` to reconcile all three pairwise: lock↔catalog (entry exists
both sides? ids/genesis/sha/location agree?), catalog↔disk (file present?
hash matches?), and lock↔disk (the v1 check, kept). Every discrepancy is
classified to a named finding with a repair pointer — `heal` for stale lock
locations, `ops` for pending/crashed WAL ops, `scan` for unabsorbed manual
changes, re-push for lost bytes.

Manual files get the H12 treatment integrated: a catalog↔disk hash mismatch
on a `sync_mode = "manual"` file is **not** automatically "corrupt" — verify
reuses Task 34's freshness heuristic (mtime/size vs `last_scanned`) to
distinguish **edited-since-last-scan** (informational: "run `vault scan` to
absorb") from **corrupt** (mtime+size unchanged but bytes differ — the bytes
rotted under us; hard finding, exit 5 class). git-mode files keep v1's strict
rule: any mismatch is corruption.

Verify also **spot-checks the WAL chain**: it runs `wal.Read`'s full chain
verification (cheap — the WAL is small relative to blobs) and re-hashes a
sample of entries against their recorded chain hashes, reporting
`TV-FED-03`-class findings on a break, plus cross-checks that files with
pending intents are excluded from corruption verdicts (an in-flight op
legitimately leaves intermediate state). Verify is read-only: it never
repairs — it diagnoses and points (hard-fail honesty, no silent fixes).

## Context

### Related packages

- `cmd/tailvault` + the verify engine (Task 23 location) — **modified here.**
- `internal/catalog` (28), `internal/wal` (29), `internal/ingest` (34 —
  freshness classification reuse), `internal/identity` (30 — lock/catalog
  genesis cross-check).
- `internal/tserr` — existing `TV-OBJ-01`, Task 32's `TV-FED-03`.

### Prerequisites

- [ ] Tasks 28, 29, 34 merged; Task 23's verify green.
- [ ] SPEC v2 §9–§11, §15 re-read (field-level agreement rules; which exit
  bucket each finding class maps to).

## Changes Required

### internal/verify/threeway.go (extending Task 23's package)

- **File:** `internal/verify/threeway.go`
- **Action:** create (modify the existing verify package)
- **Purpose:** the reconciliation + classification engine.

```go
package verify

type FindingKind int

const (
	OK FindingKind = iota
	LockOnlyEntry      // in lock, absent from catalog → heal/repush pointer
	CatalogOnlyEntry   // in catalog, not in lock → informational (manual file)
	FieldMismatch      // id/genesis/sha/location disagree lock↔catalog
	MissingOnDisk      // catalog entry, no file → TV-OBJ class
	EditedSinceScan    // manual file; hash drift + mtime/size moved → run scan
	Corrupt            // bytes differ, freshness says no edit → TV-OBJ class
	PendingOpState     // intermediate state explained by a pending WAL intent → run ops
	ChainBroken        // WAL hash-chain failure → TV-FED-03 class
	GenesisInvalid     // sha256(genesis) != id in lock or catalog entry
)

type Finding struct {
	Kind   FindingKind
	Path   string
	ID     string // short form in display
	Detail string // human diagnosis + repair pointer
}

// ThreeWay reconciles lock ↔ catalog ↔ disk for one vault and spot-checks
// the WAL chain. Read-only. Lock may be nil (non-git/manual-only vault →
// catalog↔disk only); Catalog may be nil (non-federated vault → v1 verify).
func ThreeWay(ctx context.Context, lk *lock.Lock, cat *catalog.Catalog,
	root string, log *wal.Log, opt Options) ([]Finding, error)
```

Implementation Notes:

- **Pending-intent suppression first:** before classifying any per-file
  discrepancy, load `wal.Pending`; a file whose id appears in a pending
  intent gets exactly one `PendingOpState` finding ("op <short> in flight —
  run `tailvault ops`") and no corruption verdict — half-executed ops are
  the WAL's job, not corruption.
- **Edited-vs-corrupt:** call into the same heuristic code path as Task 34
  (`ingest` classification) — do not fork the logic; a divergence between
  scan's and verify's verdicts on the same file is itself a bug class.
- **Genesis cross-check:** for every entry carrying an id, verify
  `identity.Verify(genesis, id)` on both the lock copy and the catalog copy,
  and that the two records are byte-identical — `GenesisInvalid` otherwise.
  This is the self-certification property earning its keep.
- **WAL spot-check:** full `wal.Read` chain verification (it already verifies
  every link); "spot-check" additionally re-decodes a random sample of K
  entries and re-derives their hashes independently of the cached chain walk
  (defense against a buggy fast path). K configurable, default 16.
- **Exit mapping at the boundary:** any `Corrupt`/`MissingOnDisk` → exit 5;
  any `ChainBroken` → exit 6; only informational findings
  (`EditedSinceScan`, `CatalogOnlyEntry`, `PendingOpState`) → exit 0 with
  warnings. Mixed findings take the most severe.
- Remote verify (vault on a node) streams hashes through the backend exactly
  as v1 does; the remote-sha256 short-circuit optimization is Block 4's
  task 40 — keep the seam it will slot into visible.

### cmd/tailvault/verify.go

- **File:** `cmd/tailvault/verify.go`
- **Action:** modify
- **Purpose:** wire the 3-way path, grouped output (one section per
  FindingKind with repair pointers), `--json`, and the severity exit mapping.

## Implementation Checklist

- [ ] Pairwise reconciliation lock↔catalog↔disk with the finding taxonomy.
- [ ] Pending-intent suppression before any corruption verdict.
- [ ] Edited-vs-corrupt via Task 34's shared heuristic (no forked logic).
- [ ] Genesis self-certification + lock/catalog record byte-equality check.
- [ ] WAL chain verification + K-sample independent re-hash.
- [ ] Severity → exit mapping; grouped human output + `--json`.
- [ ] Nil-lock / nil-catalog degradation paths (manual-only / non-federated).

## Testing Requirements

`internal/verify/*_test.go` (temp vault + `FSBackend`; fault-injected
fixtures):

- **Clean vault:** zero findings; exit 0.
- **Each finding kind has a dedicated fixture:** lock entry missing from
  catalog; catalog-only manual file; location mismatch lock↔catalog;
  catalog entry with no disk file; manual file edited (mtime moved) →
  `EditedSinceScan`, exit 0 + warning; manual file rewritten with restored
  mtime+size → `Corrupt`, exit 5; git-mode file with any drift → `Corrupt`;
  tampered WAL entry → `ChainBroken`, exit 6; perturbed genesis in the lock →
  `GenesisInvalid`.
- **Pending suppression:** crash-injected op (intent, no done) leaves
  intermediate state → exactly one `PendingOpState`, no `Corrupt`.
- **Heuristic parity:** the same fixture classified by `vault scan` and by
  `verify` yields the same edited/suspect verdict (shared-code regression).
- **Degradation:** nil catalog reproduces v1 verify results; nil lock checks
  catalog↔disk only.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- A crash injected between any two steps of the write-ahead ordering is
  detected by verify and attributed to the pending op (not misreported as
  corruption).
- Manual-file edits and corruption are distinguished per H12; git files stay
  strict.
- WAL tampering surfaces as a `ChainBroken` finding (exit 6).
- Verify never mutates anything; every finding carries a repair pointer
  (heal / ops / scan / re-push).

## Related Proposal Sections

> crash anywhere = detectable + repairable by `verify`/`heal`. 3-way verify:
> lock ↔ catalog ↔ disk. No distributed transactions — saga + WAL +
> reconciliation is the whole model.

> verify distinguishes "corrupt" from "edited since last scan" via mtime/size
> + `last_scanned`.

## Notes & Considerations

- **Gotcha:** suppression order is load-bearing — classify pending-op state
  *before* corruption, or every crash-recovery test will scream corruption.
- **Gotcha:** hashing every blob in a big vault is slow; keep v1's behavior
  (full hash) as the default but structure the loop so task 40's remote
  short-circuit drops in without restructuring.
- **For Next Task:** Task 39's harness leans on verify as its oracle — most
  crash/tamper integration tests end with "verify reports exactly X".
- Log every new discrepancy class discovered while building fixtures in
  `EDGE-CASES.md`.
- **Prev:** [task-37-ops-command](./task-37-ops-command.md) ·
  **Next:** [task-39-fed-test-harness](./task-39-fed-test-harness.md)
