# Task 29: internal/wal — Hash-Chained Per-Node WAL, Intent Lifecycle & WAL-as-Lock

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Per-node WAL (write-ahead log) — the concurrency + partial-failure model"; Part II task breakdown → 3.3 · **Block:** 3 — Vault catalog + federation core · **Estimated Effort:** 2 ideal eng-days · **Dependencies:** Task 27 (SPEC v2 §10 WAL schema + hash-chain rule), Task 28 (`catalog` file IDs referenced by `blob_refs`) · **Type:** Implementation

## Summary

The per-node WAL is the concurrency **and** partial-failure model for the
entire federation: every mutating op appends an **intent** entry (op id, type,
args, blob refs) before touching anything, executes, then marks done. Entries
are hash-chained — each embeds the sha256 of the previous entry — so any
tampering with history is detectable on read (tamper-evident, no consensus,
D17). This task ships `internal/wal`: append/read, chain verification, op-id
generation, the intent→receipt→execute→done lifecycle, per-blob blocking, and
pruning of done entries (journal gc, H9).

**WAL-as-lock** (D12) is the load-bearing idea: a blob has exactly one home
(single-home invariant, D3), so every op on it must touch its home node —
appending the intent record IS acquiring the per-blob lock. First appender
wins; a second op on the same blob sees the pending intent and queues or fails
with "op in flight". No coordinator, no root server, no delay windows.
Blocking is **per-blob ordering only** — ops on different blobs proceed
independently; there is no general dependency DAG.

The WAL lives on the storage node under `meta/wal/` as one immutable TOML file
per entry (SPEC v2 §10), accessed through the `Backend` interface — nodes stay
passive; all execution is client-driven over SSH. Ops carry unique ids and are
idempotent: a retry of an already-done op id is a detected no-op (dedupe).
Per §8 layering, this leaf package returns plain errors; typed sentinel errors
(`ErrOpInFlight`, `ErrChainBroken`, `ErrDuplicateOp`) let the command boundary
map to `tserr` codes (chain break → `TV-FED-03`, exit 6).

## Context

### Related packages

- `internal/wal` — **created here.**
- `internal/backend` (Task 09) — `Backend` is the transport for remote WALs;
  `FSBackend` is the test double.
- `internal/catalog` (Task 28) — file IDs used in `blob_refs`.
- Downstream: identity (Task 30 — the ingest entry IS the genesis record),
  gc (Task 36 — pending-intent skip), ops command (Task 37), verify (Task 38),
  bootstrap/scan (Tasks 33–34).

### Prerequisites

- [ ] Task 27 merged; SPEC v2 §10 frozen (entry fields, file naming, chain
  rule, state markers).
- [ ] Task 28 merged.
- [ ] Confirm layout: `meta/wal/<seq>-<op_id>.toml` + sibling
  `<seq>-<op_id>.done` / `.failed` markers (intent files immutable so the
  chain never re-hashes).

## Changes Required

### internal/wal/entry.go

- **File:** `internal/wal/entry.go`
- **Action:** create
- **Purpose:** entry type, canonical encoding, hashing per §10.

```go
package wal

type State string

const (
	StateIntent State = "intent"
	StateDone   State = "done"
	StateFailed State = "failed"
)

// Entry mirrors one meta/wal/<seq>-<op_id>.toml file (SPEC v2 §10).
type Entry struct {
	Seq       uint64            `toml:"seq"`
	OpID      string            `toml:"op_id"`     // UUIDv4 hex
	PrevHash  string            `toml:"prev_hash"` // 64-hex; 64 zeros for genesis
	OpType    string            `toml:"op_type"`   // ingest|move|delete|sync_mode|gc|roster|scan
	Args      map[string]string `toml:"args"`
	BlobRefs  []string          `toml:"blob_refs"` // file IDs this op locks
	Actor     string            `toml:"actor"`     // whois identity
	CreatedAt time.Time         `toml:"created_at"`
}

func NewOpID() string                     // crypto/rand UUIDv4
func Encode(e Entry) ([]byte, error)      // canonical bytes (frozen field order)
func Hash(e Entry) (string, error)        // sha256 over Encode(e)
func Decode(b []byte) (Entry, error)
```

### internal/wal/log.go

- **File:** `internal/wal/log.go`
- **Action:** create
- **Purpose:** the log over a `Backend`: append (lock acquire), read+verify,
  state transitions, pending queries, pruning.

```go
var (
	ErrOpInFlight  = errors.New("wal: op in flight on blob")
	ErrChainBroken = errors.New("wal: hash chain verification failed")
	ErrDuplicateOp = errors.New("wal: op id already recorded")
)

// Log reads/writes a node's WAL through its Backend (keys under "meta/wal/").
type Log struct{ B backend.Backend }

// Read lists, decodes, orders by seq, and VERIFIES THE HASH CHAIN; any broken
// link returns ErrChainBroken. Always verify-on-read — never trust blindly.
func (l *Log) Read(ctx context.Context) ([]Rec, error) // Rec = Entry + State

// AppendIntent is WAL-as-lock: it re-reads the tail, fails with ErrOpInFlight
// if any pending (state=intent) entry shares a blob ref, ErrDuplicateOp if the
// op id already exists (idempotency), then Puts the next-seq entry with
// prev_hash = hash(tail).
func (l *Log) AppendIntent(ctx context.Context, e Entry) (Rec, error)

// MarkDone / MarkFailed write the sibling state marker (never edit the entry).
func (l *Log) MarkDone(ctx context.Context, opID string) error
func (l *Log) MarkFailed(ctx context.Context, opID string, reason string) error

// Pending returns intent-state entries, optionally filtered to a blob ref —
// gc's skip set (D13) and the ops command's list source.
func (l *Log) Pending(ctx context.Context, blobRef string) ([]Rec, error)

// Prune (journal gc) deletes done entries older than keep, preserving chain
// verifiability: record the pruned head's hash in meta/wal/PRUNED so the
// remaining chain still anchors. Never prunes intent/failed entries.
func (l *Log) Prune(ctx context.Context, keep time.Duration) (int, error)
```

Implementation Notes:

- **Lifecycle (normative):** dry-run preflight (caller) → `AppendIntent` →
  receipt (the returned `Rec` is the receipt) → caller executes (blob bytes →
  catalog, per the write-ahead ordering) → `MarkDone`. Crash anywhere leaves a
  pending intent that verify/heal can detect and the ops command can retry.
- **First-appender-wins race:** two clients can race `AppendIntent` for the
  same seq. After `Put`, re-`List` and re-read the claimed seq: if a different
  op id won the slot (backend `Put` dedup means the first write sticks), the
  loser retries at the next seq; if the winner shares a blob ref, the loser
  returns `ErrOpInFlight`. Append is therefore append-then-verify, never
  fire-and-forget.
- **Idempotency:** `AppendIntent` with an op id already present (any state)
  returns `ErrDuplicateOp` + the existing record so retries are detected
  no-ops; `MarkDone` of an already-done op is a silent success.
- Seq is zero-padded to 12 digits in keys so backend `List` order is chain
  order.
- Queue-vs-fail on `ErrOpInFlight` is the **caller's** policy (interactive
  commands may wait/poll; scripts fail fast) — the package only detects.
- Serverless invariant: no goroutine, no watcher, no daemon — every function
  is a plain client-driven call.

## Implementation Checklist

- [ ] `Entry` + canonical `Encode`/`Hash`/`Decode`; `NewOpID`.
- [ ] `Read` with mandatory chain verification (`ErrChainBroken`).
- [ ] `AppendIntent` = per-blob lock: pending-conflict check, dup check,
  append-then-verify race resolution.
- [ ] `MarkDone`/`MarkFailed` via immutable sibling markers.
- [ ] `Pending` (all + per-blob).
- [ ] `Prune` of done entries with `PRUNED` chain anchor.

## Testing Requirements

`internal/wal/*_test.go` (all against `backend.FSBackend` — never a real node):

- **Lifecycle:** intent → done round-trip; entry file bytes unchanged by
  `MarkDone`.
- **Chain:** N appends → `Read` verifies; flip one byte in any entry file →
  `ErrChainBroken`; reorder/delete a middle entry → `ErrChainBroken`.
- **WAL-as-lock:** pending intent on blob X blocks a second intent on X
  (`ErrOpInFlight`); an intent on blob Y proceeds; after `MarkDone` X unlocks.
- **Race:** two interleaved `AppendIntent`s for the same seq — exactly one
  wins the slot; loser retries or gets `ErrOpInFlight` per blob overlap.
- **Idempotency:** re-append same op id → `ErrDuplicateOp` + existing rec;
  double `MarkDone` is a no-op.
- **Prune:** done entries older than keep removed; chain still verifies via
  the `PRUNED` anchor; intent/failed entries survive.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- Hash-chain tamper of any kind is detected on read.
- Appending an intent acquires the per-blob lock: concurrent ops on one blob
  serialize; ops on different blobs are independent.
- Retried ops dedupe by op id; crash between any lifecycle step leaves a
  detectable pending intent.
- Done-entry pruning preserves chain verifiability.

## Related Proposal Sections

> Every mutating op: **dry-run preflight** (fail early) → append **intent**
> record (op id, type, args, blob refs) → receipt → execute → confirm → mark
> done. Ops are idempotent with unique ids (retry-safe, dedupe).

> **WAL-as-lock:** … appending the intent IS acquiring the per-blob lock.
> First appender wins; later ops queue or fail "op in flight". No coordinator,
> no root server, no delay windows. … Blocking is **per-blob ordering** only.
> Done-entries are pruned by a journal GC.

## Notes & Considerations

- **Gotcha:** the chain hash covers the **intent file bytes only**; state
  markers are outside the chain by design — otherwise `MarkDone` would
  invalidate every later link.
- **Gotcha:** `Prune` without the `PRUNED` anchor would make every surviving
  chain unverifiable from genesis. Test this path specifically.
- **For Next Task:** Task 30 derives file IDs from **ingest WAL entries** — the
  genesis record is `{content_sha256, original_path, ingest_op_id, origin_node}`
  drawn from an `op_type = "ingest"` entry's fields.
- Append concurrency findings (races seen under `-race`, backend quirks) to
  `EDGE-CASES.md`.
- **Prev:** [task-28-catalog](./task-28-catalog.md) ·
  **Next:** [task-30-identity](./task-30-identity.md)
