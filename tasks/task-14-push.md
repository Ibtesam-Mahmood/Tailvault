# Task 14: `tailvault push` — the critical path (preflight, dedup, upload, lock-last)

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** "Push flow (the critical path)" (sequence diagram); Error model (preflight-first ordering, bucketed exit codes); Open Questions Q7 (pusher identity) · **Block:** 1 — MVP · **Estimated Effort:** 1.5 days · **Dependencies:** Task 09 (provides `Backend` + preflight `Stat`/`ping`), Task 04 (provides `internal/lock` read/write + canonical form), Task 05 (provides the rule engine), Task 06 (provides pointer parse — for already-cleaned working files), Task 07 (provides `internal/tserr` codes + exit buckets) · **Type:** Implementation

## Summary

`tailvault push` is **the** load-bearing command and the proposal's named critical path: a green push must *guarantee* the bytes landed, or fail loudly leaving nothing half-done. This task implements the push sequence diagram precisely and in order.

The ordering is the whole point. (1) **Preflight first** — `tailscale status` / `ping` (and a backend `Stat` reachability probe) the target node; if it is unreachable, hard-fail with a `tserr` code (`TV-NODE-01`, or `TV-NET-*`) and a non-zero exit *before any upload or lock mutation*, so a node-down push leaves no partial upload and an **unadvanced** lock. (2) Scan the tree for vault-managed files via the rules, hash each → sha; if sha equals the lock's sha it is a **no-op** (this is what makes a move/rename a zero-byte operation); otherwise `Stat objects/<sha>` and `Put` only if missing, then update the lock entry. (3) Deleted files drop their entry and mark the old sha for GC unless `preserve` (the actual sweep is Task 16). (4) Stamp the pusher via `tailscale whois`, falling back to git identity (Q7). (5) Write `tailvault.lock` **last**, so the repo never gets ahead of storage.

Correctness is defined by three behaviours the tests must pin: re-pushing an unchanged tree issues **zero** `Put` calls (dedup), a move/rename with identical content issues **zero** transfer and merely renames the lock key, and a node-down preflight exits non-zero with the lock unadvanced.

## Context

### Related packages
- `internal/backend` (Task 09) — `Stat`/`Put`; the preflight reachability check.
- `internal/lock` (Task 04) — load current lock, mutate entries, write canonical form last.
- `internal/rules` (Task 05) — enumerate managed files.
- `internal/status` (Task 13) — reuse `ScanTree` (content hashing, pointer-aware).
- `internal/pointer` (Task 06) — resolve a working file's sha when it is already a clean pointer.
- `internal/tserr` (Task 07) — `TV-NODE-01`, `TV-NET-01/02`, `TV-NODE-02`; exit buckets 3/4.
- `internal/tailscale` (Task 08) — `status`/`ping`/`whois`.
- `cmd/tailvault/push.go` — Cobra command.

```mermaid
sequenceDiagram
    participant TV as tailvault push
    participant TS as tailscale
    participant N as node
    TV->>TS: status/ping target
    alt unreachable
        TS-->>TV: down → tserr TV-NODE-01, exit 4 (NO uploads, lock unchanged)
    end
    TV->>TV: scan rules → hash each file → sha
    loop each managed file
        alt sha == lock sha
            TV->>TV: no-op (covers move/rename)
        else changed/new
            TV->>N: Stat objects/sha
            opt missing
                TV->>N: Put objects/sha
            end
            TV->>TV: update lock entry; mark old sha for GC (history-off)
        end
    end
    TV->>TV: deleted files → drop entry; mark sha for GC unless preserve
    TV->>TS: whois → pusher (fallback git user.email)
    TV->>TV: write tailvault.lock LAST
```

### Prerequisites
- [ ] Tasks 04, 05, 06, 07, 09 merged.
- [ ] Task 13's `ScanTree` available for reuse (content hashing + pointer handling).

## Changes Required

**File:** `internal/push/push.go`
**Action:** Create.
**Purpose:** Orchestrate the full push sequence in the proposal's order.

```go
package push

type Options struct {
    Branch   string // --branch (informational/lock provenance)
    DryRun   bool
}

type Result struct {
    Uploaded []string // shas Put this run
    Deduped  []string // shas already present (Stat hit, no Put)
    Renamed  []string // lock keys renamed (move/rename, zero transfer)
    Dropped  []string // entries removed (deletions)
    MarkedGC []string // shas marked for sweep (Task 16)
}

func Run(ctx, root string, cfg, lk, be, ts, opts) (Result, error) {
    // 1. PREFLIGHT FIRST — no work before this returns nil.
    if err := preflight(ctx, ts, be, loc); err != nil { return Result{}, err } // tserr
    // 2. scan tree → path→sha (status.ScanTree, pointer-aware)
    // 3. diff vs lock:
    //    - sha == lock[path].sha → no-op
    //    - path new OR sha changed → Stat; Put if missing; update entry
    //    - sha present at a DIFFERENT path in lock & old path gone → rename key, zero transfer
    // 4. deletions: lock path absent from tree → drop entry; mark sha for GC unless preserve
    // 5. pusher := ts.Whois(self) ?? gitIdentity()
    // 6. write lock LAST (lock.Save canonical) — unless DryRun
}
```

Implementation Notes:
- **Preflight is a hard gate.** `preflight` runs `tailscale status` (→ `TV-NET-01/02` if daemon down / logged out), confirms the location's node is an online peer + `ping`s it (→ `TV-NODE-01`), and does a cheap backend reachability `Stat` of the objects prefix (→ `TV-NODE-02` if base_path unwritable). Return on the first failure; do not proceed.
- **Dedup** has two layers: (a) sha == lock sha → skip entirely, no `Stat`, no `Put`; (b) sha ≠ lock sha but `Stat objects/<sha>` hits → update lock but **no `Put`** (another path/branch already uploaded that content).
- **Move/rename detection:** build a reverse index `sha → []lockPath`. If a new tree path has a sha that exists in the lock under a path now absent from the tree, treat it as a key rename (carry `pushed_at`/`pusher`/flags forward) with **zero** transfer rather than an add+delete.
- **`Put` only after `Stat` miss**, and (per the risk table) verify with a post-`Put` `Stat` that the blob now exists before recording it in the lock — never record a sha the node doesn't actually hold.
- **GC marking only** here: record marked shas (e.g. into `meta/manifest.json` marks or the `Result`), do **not** delete — the sweep is Task 16. Respect `preserve` (override) so preserved shas are never marked.
- **Lock written last**, via Task 04's canonical writer, so a crash anywhere earlier leaves the committed lock untouched.
- `whois` failure is non-fatal → fall back to `git config user.email` (Q7).

Key Considerations:
- Idempotency: a second `push` over an unchanged tree must produce `Result{}` with empty `Uploaded`/`Renamed`/`Dropped` and issue **zero** `Put` calls.
- `--dry-run` performs preflight + scan + diff and reports the plan but writes neither blobs nor lock.

---

**File:** `cmd/tailvault/push.go`
**Action:** Modify (replace stub).
**Purpose:** Resolve config/location/backend, call `push.Run`, map errors to exit buckets, print a summary.

```go
res, err := push.Run(ctx, root, cfg, lk, be, ts, opts)
if err != nil { return err } // tserr carries exit code via main's mapper
fmt.Printf("uploaded %d, deduped %d, renamed %d, dropped %d\n",
    len(res.Uploaded), len(res.Deduped), len(res.Renamed), len(res.Dropped))
```

Implementation Notes:
- The command itself adds nothing to the ordering — all the guarantees live in `push.Run`. Keep the command thin so the `pre-push` hook (Task 19) can call the same `Run`.

Key Considerations:
- Exit-code mapping is centralised in `main` via `tserr` (Task 07): node-down → exit 4, tailnet-down → exit 3. The `pre-push` hook surfaces the same code so a blocked push reads obviously.

## Implementation Checklist
- [ ] Preflight runs first and aborts (tserr, exit 3/4) before any `Put` or lock write.
- [ ] sha == lock sha → no-op (no `Stat`/`Put`); zero `Put` on unchanged re-push.
- [ ] sha changed/new → `Stat`, `Put` if missing, post-`Put` `Stat` verify, then update entry.
- [ ] Move/rename (same sha, old path gone) → lock key renamed, zero transfer.
- [ ] Deletions drop entries and mark sha for GC unless `preserve`.
- [ ] Pusher from `tailscale whois`, falling back to git `user.email`.
- [ ] `tailvault.lock` written **last**, canonical form.
- [ ] `--dry-run` writes nothing.

## Testing Requirements

Go table/integration tests in `internal/push/push_test.go` using the **stub Backend from Task 09** (counts `Stat`/`Put`/`Delete` calls; scriptable hit/miss) and the **tailscale fixture from Task 08** (scriptable reachable/down + whois).

| Case | Setup | Expect |
|---|---|---|
| Dedup / unchanged re-push | tree == lock; all shas present | **zero** `Put`; `Result.Uploaded` empty; lock byte-identical |
| New file | tree adds `c.pdf` (sha not on node) | exactly one `Put objects/<sha>`; lock gains entry; written last |
| Content present elsewhere | new path, sha already on node (`Stat` hit) | lock updated, **zero** `Put` |
| Move/rename | lock `old.pdf`(X); tree `new.pdf`(X), `old.pdf` gone | **zero** transfer; lock key renamed old→new; flags/pushed_at carried |
| Deletion + auto_delete | lock `d.pdf`; absent from tree; not preserved | entry dropped; sha in `MarkedGC`; no `Delete` issued (sweep = Task 16) |
| Deletion + preserve | override `preserve=true` | entry dropped but sha **not** in `MarkedGC` |
| Node down → abort | tailscale fixture = unreachable | `push.Run` returns tserr `TV-NODE-01`; exit 4; **zero** `Put`; lock unadvanced (unchanged on disk) |
| Tailnet down | fixture = daemon not running | `TV-NET-01`; exit 3; no work |
| whois fallback | whois errors | `pusher` == git `user.email` |
| Put verify failure | `Put` succeeds but post-`Stat` misses | error; lock entry **not** recorded for that sha |
| dry-run | `--dry-run` over a changed tree | plan reported; zero `Put`; lock unchanged on disk |

Stubs/fixtures:
- Stub `Backend` exposing call counters and a `present map[sha]bool` to drive `Stat` hits/misses and assert `Put` counts.
- Tailscale fixture toggling reachability and returning a `whois` identity (or error for the fallback case).
- Temp repo trees for the move/rename, delete, and new-file scenarios.

## Validation Checklist
- [ ] `go build ./...`, `go test ./...`, `go vet ./...` pass.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to `CHANGELOG.md` in the same commit (per CONTRIBUTING.md).

## Acceptance Criteria
- Push preflights the node first and, on unreachable, exits non-zero (3/4) with **no** upload and an **unadvanced** lock.
- Unchanged re-push issues zero `Put`; move/rename transfers zero bytes and renames the lock key.
- New/changed content is `Put` only on `Stat` miss, verified by a post-`Put` `Stat`, before being recorded.
- Deletions drop entries and mark shas for GC unless `preserve`; pusher stamped via `whois` (git fallback).
- `tailvault.lock` is written last, in canonical form.
- VERSION + CHANGELOG bumped in the same commit.

## Related Proposal Sections
- **Push flow (the critical path)** — the full sequence: preflight → scan → per-file hash/Stat/Put → deletions → GC mark → `whois` stamp → "write `tailvault.lock`; exit 0 → git proceeds."
- **Error model** — "Preflight-first ordering guarantees a node-down failure leaves no partial upload and an unadvanced lock."
- **Risk Assessment** — "Upload blobs before writing/committing lock; verify Stat post-Put."
- **Open Questions Q7** — "`tailscale whois`… fall back to git identity."

## Notes & Considerations
- **Gotcha:** the lock must be the **last** write — any earlier crash must leave the committed lock pristine so the repo never gets ahead of storage.
- **Gotcha:** GC here is *mark-only*; do not call `Backend.Delete`. The sweep, keyed on every branch's lock, is Task 16.
- **Gotcha:** post-`Put` `Stat` verification is non-optional — recording a sha the node doesn't actually hold is exactly the "lock ahead of storage" failure the design forbids.
- **For Next Task:** Task 15 (`pull`) is the inverse (smudge direction): `Get` the blobs the tree needs and verify their sha against the lock.
- Prev: [task-13-status.md](./task-13-status.md) · Next: [task-15-pull.md](./task-15-pull.md)
