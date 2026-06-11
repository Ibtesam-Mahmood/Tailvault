# Task 44: `vault mv` — Intra- and Cross-Location Moves

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Moves" (Vision §4), "Security & transport" (move transport), "Resolution & reachability" (`moved_to` forwarding), "Per-node WAL" · **Block:** 4 — Remote interaction CLI · **Estimated Effort:** 2 ideal eng-days · **Dependencies:** Task 29 (`internal/wal`), Task 28 (`internal/catalog`), Task 30 (`internal/identity` — IDs survive moves), Task 32 (resolution + `moved_to` semantics), Task 35 (lock v2 + `heal` — the git-side consumer), Task 37 (`ops` — pending-op retry), Task 40 (`HashObject`), Task 46 (auth gate) · **Type:** Implementation

## Summary

`vault mv` relocates a federated file: **intra-location** (rename within one
node's tree — a catalog/path change, no bytes move) or **cross-location** (bytes
travel **node-to-node over SSH/rsync across the tailnet**, D11 — Taildrop was
rejected as inbox/staging delivery, not path-to-path). In both cases the file
**ID never changes** — only the logical path does (D19 dual addressing). Git
repos that referenced the old home discover the move on next sync: pull warns,
`heal` (Task 35) rewrites the lock through the logical layer.

Cross-location is the system's first **two-node mutation**, so the WAL lock is
taken on **BOTH ends**: an intent on the source ("moving out, id X, to dest D")
and an intent on the destination ("receiving id X from S"). Either pending
intent blocks concurrent ops on the blob (D12/D13: gc skips it, a second mv
fails "op in flight"). Execution order: source intent → dest intent → transfer
(node-to-node rsync, client-orchestrated — the client drives but the bytes flow
peer-to-peer) → verify at dest (`HashObject`) → dest catalog add → dest done →
source catalog: replace entry with a **`moved_to` record** → source done. The
`moved_to` record stays behind as a **forwarding pointer** so resolution finds
the file even while the new home is offline.

If the destination is down at the start, the op fails preflight; if it goes
down mid-flight, the intents remain pending and `tailvault ops retry` (Task 37)
resumes idempotently. Moves mutate both nodes, so the op is **password-gated**
(D9). Single-home invariant holds throughout: the blob is never authoritative
in two places — the dest entry only becomes live after verification, and the
source entry becomes a forwarder in the same lifecycle.

## Context

### Related packages

- `cmd/tailvault` — **created here:** `vault mv` subcommand.
- `internal/wal` (Task 29) — dual intents, pending-op blocking, idempotent ids.
- `internal/catalog` (Task 28) — dest add, source `moved_to` replacement.
- `internal/resolve` (Task 32) — consumes `moved_to` forwarding (already built);
  this task produces the records it follows.
- `internal/backend` — a **node-to-node transfer helper is added here** (see
  below).
- `internal/auth` (Task 46) — gate on both nodes.

### Prerequisites

- [ ] Tasks 27–32, 35, 37, 40, 43 merged.
- [ ] SPEC v2 `moved_to` record shape confirmed (id, dest location, dest path,
  op id, timestamp).

## Changes Required

### cmd/tailvault/vault_mv.go

- **File:** `cmd/tailvault/vault_mv.go`
- **Action:** create
- **Purpose:** the command.

```go
// tailvault vault mv <src logical-path | id> <dest location>/<dest-path>
// flags: --on-conflict=copy|rename|stop (dest name collision, reuse Task 43
//        machinery), --json
func runVaultMv(cmd *cobra.Command, args []string) error {
	// resolve src (home node) -> intra vs cross by comparing locations
	// auth.Gate(srcNode) [+ destNode if cross]
	// dry-run both ends -> intents both ends -> transfer -> verify -> catalogs
	// -> done both ends; moved_to left at source (cross) or path rewritten (intra)
}
```

Implementation Notes:

- **Intra-location:** single WAL intent on the one node; catalog path update;
  no transfer; `moved_to` is NOT left behind for a same-node rename — the
  catalog's entry itself records the new path (resolution still finds it
  because the home didn't change). Old-path lookups from stale locks get the
  WARN+heal flow.
- **Cross-location transfer:** orchestrate `rsync` (fall back to
  `ssh src cat | ssh dest write`-style streaming when rsync is absent) from
  source node directly to destination node — the client must **not** relay the
  bytes through itself. Both legs ride Tailscale SSH/WireGuard; never roll our
  own transport (D8).
- **Verification before cut-over:** dest `HashObject` must equal the catalog
  sha (git files) before the dest catalog entry is written; only then is the
  source entry demoted to `moved_to`. A failure mid-way leaves: pending
  intents + bytes possibly at dest temp path — all repairable, nothing lost,
  nothing silently succeeded.
- **`moved_to` record** (cross): written into the source catalog (and WAL) as
  the forwarding pointer; resolution (Task 32) already follows it, including
  when the dest is offline ("file moved to office-nas, currently
  unreachable" → TV-FED-flavored report, not TV-OBJ).
- **ID invariance:** assert in code (and tests) that the dest entry carries the
  identical id + genesis record — `mv` never re-mints.
- Manual files: `mv` of an edited-since-scan manual file moves the bytes as
  they are on disk and re-hashes at dest (sha may differ from catalog) — record
  the fresh sha at dest, count it as a scan (`last_scanned` updated).
- SPEC §8 layering as usual; partial failures surface as pending ops, not
  half-errors.

### internal/backend/transfer.go

- **File:** `internal/backend/transfer.go`
- **Action:** create
- **Purpose:** node-to-node transfer helper.

```go
// Transfer copies one object from src node to dest node directly (rsync over
// ssh, streaming fallback), client-orchestrated, peer-to-peer bytes.
func Transfer(ctx context.Context, src, dest NodeSpec, key string) error
```

The stub/harness implementation copies between the two stub roots so Task 50
tests run with no real node or rsync.

## Implementation Checklist

- [ ] Intra-location rename: single-node WAL lifecycle, catalog path update.
- [ ] Cross-location: dual intents, peer-to-peer transfer, verify-then-cut-over,
  `moved_to` forwarder at source.
- [ ] ID + genesis unchanged through any move (asserted).
- [ ] Dest-name conflict reuses Task 43's prompt/`--on-conflict`.
- [ ] Auth gate on every mutated node before its intent.
- [ ] Mid-flight failure → pending ops visible in `ops`, `ops retry` completes
  idempotently.
- [ ] gc (Task 36) skip of in-flight blob holds (covered again in Task 50).

## Testing Requirements

Against the Task 39 harness (stub backends only):

- **Intra rename:** path changes, id/sha/bytes untouched, no transfer calls.
- **Cross move happy path:** bytes at dest, dest catalog live, source has
  `moved_to`, id identical, WAL done on both ends.
- **Forwarding:** after the move, take the dest member down → resolution via
  `moved_to` reports the new home as unreachable (TV-FED flavor), not missing.
- **Dest down at start:** preflight failure, zero intents anywhere.
- **Dest dies mid-transfer (fault injection):** pending intents on both ends;
  `ops retry` after the member returns completes the move exactly once.
- **Concurrent op:** second mv on the same id while pending → "op in flight".
- **Auth rejection** on either node → no intent on either node.
- **Edited manual file:** moved as-is; dest catalog sha = fresh hash,
  `last_scanned` updated.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- Both move kinds work end-to-end on the harness; the file ID is provably
  unchanged.
- Cross moves are WAL-locked on both ends and leave a `moved_to` forwarder the
  resolution engine follows even with the new home offline.
- Bytes flow node-to-node (the stub transfer helper records no client-side
  relay), and a half-done move is always either retryable or cleanly diagnosed
  — never silent, never duplicated, never two live homes.
- Password-gated on every mutated node.

## Related Proposal Sections

> **Moves** — files move between/within locations; the next git sync resolves
> the new home through the logical layer (pull warns; `heal` rewrites the
> lock).

> Move transport = **node-to-node SSH/rsync over the tailnet** (Taildrop
> rejected: inbox/staging delivery, not path-to-path).

> the source's `moved_to` WAL/catalog record doubles as a forwarding pointer
> (finds files whose new home is currently offline).

## Notes & Considerations

- **Gotcha:** the order *dest-live-then-source-forwarder* is what preserves
  single-home: reversing it creates a window where the file has zero homes.
- **Gotcha:** rsync flag sets differ across versions — pin to a conservative
  flag set (`rsync -a --partial` class) and treat its absence as a clean
  fallback, not an error.
- **Gotcha:** chained moves (A→B, later B→C) must not require updating A's old
  forwarder — resolution follows hops; document the hop limit chosen by Task 32.
- **For Next Task:** Task 45 (`vault rm` + sync-mode) is the remaining
  single-node mutation pair, reusing this task's gate + WAL patterns.
- **Prev:** [task-43-vault-put](./task-43-vault-put.md) ·
  **Next:** [task-45-vault-rm-syncmode](./task-45-vault-rm-syncmode.md)
