# Task 15: `tailvault pull` — fetch needed blobs (smudge direction) with integrity check

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** CLI surface (`tailvault pull` — "fetch blobs the current tree/branch needs"); Testing Strategy — "Integrity: corrupt a stored blob → `pull`/`verify` detects mismatch"; Open Questions Q6 (eager fetch for v1) · **Block:** 1 — MVP · **Estimated Effort:** 1 day · **Dependencies:** Task 09 (provides `Backend.Get` + preflight + stub), Task 04 (provides `internal/lock` read — the source of truth for needed shas), Task 06 (provides pointer parse — to know which working files are unmaterialised) · **Type:** Implementation

## Summary

`tailvault pull` is the smudge direction: it materialises the real bytes a clone needs from the content-addressed store. For each `tailvault.lock` entry whose working file is currently a **pointer** or **missing**, `pull` issues `Get objects/<sha>` and writes the real bytes to the working path, then **verifies** that the sha256 of the fetched content matches the locked `sha256`. A mismatch or a missing blob is a hard failure with `tserr TV-OBJ-01` (integrity bucket, exit 5) — the design's "hard-fail on missing object" guarantee.

Per Open Question Q6, v1 is **eager**: `pull` fetches everything the current tree/branch's lock references that isn't already materialised, rather than deferring to first access. This keeps the implementation simple and checkouts correct at the cost of up-front transfer — lazy fetch is a tracked future option.

Files whose working content is already present and correct (sha already matches the lock) are skipped — `pull` is idempotent and only transfers what is genuinely absent or still a pointer. The end state: after `pull`, every locked file in the working tree holds its real, integrity-verified bytes.

## Context

### Related packages
- `internal/lock` (Task 04) — `Load()` → entries; each `Entry.Path`, `Entry.SHA256`, `Entry.Location`. Read-only.
- `internal/backend` (Task 09) — `Get(ctx, "objects/"+sha, w)`; preflight reachability.
- `internal/pointer` (Task 06) — detect/parse a `tailvault.v1` pointer to know a file is unmaterialised and read its expected sha.
- `internal/tserr` (Task 07) — `TV-OBJ-01` (integrity/missing blob, exit 5); `TV-NODE-*`/`TV-NET-*` from preflight.
- `internal/hash` — sha256 of fetched bytes for verification.
- `cmd/tailvault/pull.go` — Cobra command.

```mermaid
flowchart TD
    L[lock entries] --> F{working file<br/>pointer or missing?}
    F -- no, real bytes correct --> S[skip]
    F -- yes --> G[Get objects/sha → temp]
    G -- missing on node --> E1[tserr TV-OBJ-01 exit 5]
    G -- got bytes --> V{sha256(bytes)==lock sha?}
    V -- no --> E2[tserr TV-OBJ-01 exit 5]
    V -- yes --> W[atomic write to working path]
```

### Prerequisites
- [ ] Tasks 04, 06, 09 merged.
- [ ] Preflight available from Task 09/08 (pull contacts the node, so it preflights like push).

## Changes Required

**File:** `internal/pull/pull.go`
**Action:** Create.
**Purpose:** Orchestrate the eager fetch + per-blob integrity verification.

```go
package pull

type Result struct {
    Fetched []string // paths materialised this run
    Skipped []string // already-correct paths
}

func Run(ctx, root string, lk lock.Lock, be backend.Backend, ts) (Result, error) {
    // preflight node (tserr on unreachable) — fetch reads from the node
    if err := preflight(ctx, ts, be); err != nil { return Result{}, err }
    for _, e := range lk.Entries {
        if materializedAndCorrect(root, e) { skip; continue } // sha already matches
        // Get objects/<sha> into a temp file, hashing as we stream
        gotSHA, err := getAndHash(ctx, be, "objects/"+e.SHA256, tmp)
        if err != nil { return tserr.ObjMissing(e.SHA256, e.Path) }   // TV-OBJ-01
        if gotSHA != e.SHA256 { return tserr.ObjMismatch(e.SHA256, gotSHA) } // TV-OBJ-01
        atomicRename(tmp, filepath.Join(root, e.Path))
    }
}
```

Implementation Notes:
- **Eager (Q6):** iterate **all** lock entries for the current tree/branch; fetch every one not already materialised-and-correct. No lazy access shim in v1.
- **Hash while streaming:** wrap the `Get` writer in a `sha256` `io.MultiWriter` to the temp file, so verification costs no second pass over the bytes.
- **Atomic write:** download to a temp file in the same directory, verify, then `os.Rename` into place — a failed/corrupt fetch must never leave a half-written real file shadowing a pointer.
- **`materializedAndCorrect`:** if the working file exists, isn't a pointer, and hashes to the lock sha, skip it (idempotent). If it's a pointer or missing, fetch.
- **Missing vs mismatch** both map to `TV-OBJ-01` (exit 5) but with distinct messages (`missing on node` vs `sha mismatch <want>/<got>`) so the user can tell a gone blob from a corrupted one.

Key Considerations:
- Preflight first (like push): a node-down `pull` must fail cleanly (exit 4) before touching the working tree.
- Never write unverified bytes to the working path — verification gates the rename.

---

**File:** `cmd/tailvault/pull.go`
**Action:** Modify (replace stub).
**Purpose:** Resolve lock/location/backend, call `pull.Run`, map errors to exit buckets, print a summary.

```go
res, err := pull.Run(ctx, root, lk, be, ts)
if err != nil { return err } // tserr → exit 5 (integrity) / 4 (node) / 3 (net)
fmt.Printf("fetched %d, skipped %d\n", len(res.Fetched), len(res.Skipped))
```

Implementation Notes:
- Thin command; the `post-merge`/`post-checkout` hooks (Task 19) call the same `pull.Run` for eager smudge after a branch change.

Key Considerations:
- Centralised exit mapping via `tserr` — integrity failures must exit 5 so scripts/hooks can branch on "blob trouble" distinctly from "node down" (4).

## Implementation Checklist
- [ ] Preflight runs before any fetch (tserr on unreachable, exit 3/4).
- [ ] For each lock entry that is a pointer or missing, `Get objects/<sha>`.
- [ ] Verify fetched sha256 == lock sha256; mismatch → `TV-OBJ-01` (exit 5).
- [ ] Missing blob on node → `TV-OBJ-01` (exit 5).
- [ ] Already-correct files skipped (idempotent).
- [ ] Atomic temp-then-rename; no unverified bytes ever land at the working path.
- [ ] Eager: all needed entries fetched in one run (Q6).

## Testing Requirements

Go table/integration tests in `internal/pull/pull_test.go` using the **stub Backend from Task 09** (serves canned blob bytes per sha, or returns not-found) and the **tailscale fixture from Task 08** (reachable for the happy path; down for the preflight case).

| Case | Setup | Expect |
|---|---|---|
| Restores real bytes | working file is a pointer; backend serves matching bytes for its sha | working file now holds real bytes; `Fetched` includes it; sha verified |
| Idempotent skip | working file already holds correct real bytes | no `Get`; path in `Skipped` |
| Missing blob | lock sha has no `objects/<sha>` on node | `tserr TV-OBJ-01` ("missing"); exit 5; working file untouched |
| Corrupt / mismatched blob | backend serves bytes whose sha ≠ lock sha | `tserr TV-OBJ-01` ("sha mismatch"); exit 5; working path **not** overwritten (atomic temp discarded) |
| Node down → abort | tailscale fixture unreachable | preflight `TV-NODE-01`; exit 4; no `Get`; tree untouched |
| Multiple entries eager | lock has 3 pointers, all present on node | all 3 fetched in one run |
| Partial corruption stops cleanly | entry 1 ok, entry 2 mismatched | entry 1 materialised; entry 2 errors `TV-OBJ-01`; no half-written entry-2 file |

Stubs/fixtures:
- Stub `Backend` whose `Get` writes canned bytes for known shas and returns not-found otherwise; a "corrupt" variant serves bytes that hash to a different sha.
- Tailscale fixture toggling reachability for the preflight test.
- Temp working trees seeded with pointer files (Task 06 format) and, for the skip case, real correct bytes.

## Validation Checklist
- [ ] `go build ./...`, `go test ./...`, `go vet ./...` pass.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to `CHANGELOG.md` in the same commit (per CONTRIBUTING.md).

## Acceptance Criteria
- `pull` materialises real, sha-verified bytes for every pointer/missing locked file (eager, Q6).
- A missing or mismatched blob hard-fails with `TV-OBJ-01` (exit 5) and never overwrites the working path with bad bytes.
- Already-correct files are skipped; pull is idempotent.
- A node-down pull aborts at preflight (exit 4) before touching the tree.
- VERSION + CHANGELOG bumped in the same commit.

## Related Proposal Sections
- **CLI surface** — "`tailvault pull` — fetch blobs the current tree/branch needs."
- **Testing Strategy → Integration** — "Integrity: corrupt a stored blob → `pull`/`verify` detects mismatch."
- **Error model** — `TV-OBJ-01 — Expected blob <sha> missing on the node` (integrity/`pull`); exit bucket 5.
- **Open Questions Q6** — "eager (fetch all on `post-checkout`) … Recommend: eager for v1."

## Notes & Considerations
- **Gotcha:** verify **before** the rename — streaming hash + atomic temp-then-rename is what prevents a corrupt download from shadowing a good pointer.
- **Gotcha:** distinguish "missing on node" from "sha mismatch" in the message even though both are `TV-OBJ-01`; they point the user at different fixes (re-push vs `verify`/re-store).
- **Gotcha:** pull contacts the node, so it preflights exactly like push — a down node is exit 4, not exit 5.
- **For Next Task:** Task 16 (`gc`) consumes the GC marks that `push` (Task 14) records and the keep-set across all branch locks to sweep unreferenced blobs.
- Prev: [task-14-push.md](./task-14-push.md) · Next: [task-16-gc-retention.md](./task-16-gc-retention.md)
