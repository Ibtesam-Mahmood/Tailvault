# Task 09: Backend Interface + SSH Implementation

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Detailed Design → "Backend abstraction" (the Go interface block), "Storage layout on the node", "Push flow"; "Error model" · **Block:** 1 — MVP · **Estimated Effort:** 2 ideal eng-days · **Dependencies:** Task 07 (`internal/tserr` for `TV-NODE-*`/`TV-OBJ-01`), Task 08 (`internal/tailscale` for `Ping` preflight) · **Type:** Implementation

## Summary

A backend is "a path that can hold `objects/` and `refs/`." This task defines
the `Backend` interface exactly as the proposal specifies and ships the first
concrete implementation: SSH, which streams blobs over `ssh user@node` using
remote helper commands (`cat`, `dd`, `sha256sum`) and stores content at
`objects/<sha256>` under the location's base path. Because storage is
content-addressed, `Put` is a no-op when `Stat` already hits — this is the
dedup that makes moves/renames free and re-pushes cheap.

The interface is the seam the whole engine pushes/pulls through, so this task
also delivers reusable **contract tests** and a loopback/stub backend over a
temp directory that satisfies the same interface. The stub lets `push`,
`pull`, `gc`, and `verify` (later tasks) be tested without a real Tailscale node,
and guarantees the SSH backend and any future Taildrive backend share identical
semantics.

Errors are surfaced through `tserr`: an unreachable node is `TV-NODE-01`, a
reachable-but-unwritable base path is `TV-NODE-02`, and a `Get`/`Stat` for a key
that does not exist is `TV-OBJ-01`. Preflight (via `tailscale.Ping`) runs before
any transfer so a node-down failure leaves no partial upload.

## Context

### Related packages

- `internal/backend` — **created here.** `Backend` interface, `ssh` impl,
  `stub`/loopback impl, contract test helper.
- `internal/tserr` (Task 07) — `TV-NODE-01/02`, `TV-OBJ-01`.
- `internal/tailscale` (Task 08) — `Ping` for preflight.
- `internal/locations` (Task 10) — resolves a location entry into a constructed
  backend.

### Prerequisites

- [ ] Task 07 merged (`tserr`).
- [ ] Task 08 merged (`tailscale.Ping`).
- [ ] Confirm the storage layout: `objects/<sha256>`, `refs/<path-id>`,
  `meta/manifest.json` under `<base_path>/<subpath>/`.

## Changes Required

### internal/backend/backend.go

- **File:** `internal/backend/backend.go`
- **Action:** create
- **Purpose:** the interface (exact signatures from the proposal) + `Meta`.

```go
package backend

import (
	"context"
	"io"
)

// Meta is the result of Stat: existence + size.
type Meta struct {
	Exists bool
	Size   int64
}

// Backend is "a path that can hold objects/ and refs/."
type Backend interface {
	Stat(ctx context.Context, key string) (Meta, error)   // exists? size?
	Get(ctx context.Context, key string, w io.Writer) error
	Put(ctx context.Context, key string, r io.Reader) error // content-addressed: skip if Stat hits
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}
```

Implementation Notes:

- `key` is a store-relative path like `objects/9f2b1c…` or `refs/<path-id>`;
  backends join it onto `<base_path>/<subpath>`.
- `Get` writes to the supplied `io.Writer` (streaming, not buffering the whole
  blob) — important at ~1 GB.

### internal/backend/ssh.go

- **File:** `internal/backend/ssh.go`
- **Action:** create
- **Purpose:** SSH backend streaming over `ssh user@node` via remote helpers.

```go
type SSH struct {
	User     string
	Node     string // MagicDNS name or 100.x IP
	BasePath string // <base_path>/<subpath>
	Ping     func(ctx context.Context, node string) error // injected from tailscale
	// Runner seam for exec, mirroring internal/tailscale, for testability.
}

func (s *SSH) Stat(ctx context.Context, key string) (Meta, error) {
	// preflight Ping(node) -> TV-NODE-01 on failure
	// ssh ... `test -f P && stat -c %s P` (or `wc -c`) -> Meta{Exists,Size}
}
func (s *SSH) Put(ctx context.Context, key string, r io.Reader) error {
	// if Stat(key).Exists -> no-op (dedup)
	// mkdir -p parent; stream stdin -> `dd of=P` (or `cat > P.tmp` then mv);
	// optionally verify with remote `sha256sum` for objects/<sha>;
	// unwritable base_path -> TV-NODE-02
}
func (s *SSH) Get(ctx context.Context, key string, w io.Writer) error {
	// ssh ... `cat P` -> w ; missing key -> TV-OBJ-01
}
func (s *SSH) Delete(ctx context.Context, key string) error // ssh `rm -f P`
func (s *SSH) List(ctx context.Context, prefix string) ([]string, error) // ssh `ls`/`find`
```

Implementation Notes:

- **Preflight:** every method (or a shared helper) calls `s.Ping(ctx, s.Node)`
  first; a failure becomes `tserr.NodeOfflineErr(s.Node)` (`TV-NODE-01`) before
  any data moves — guaranteeing no partial upload on a down node.
- **Dedup:** `Put` calls `Stat` first; if `Exists`, return nil without
  transferring (the proposal's "no-op if Stat hits"). This covers move/rename
  and unchanged re-push with zero transfer.
- **Atomic-ish writes:** stream to a `*.tmp` then `mv` into place so a partial
  transfer never leaves a corrupt `objects/<sha>`. Optionally compare remote
  `sha256sum` against the key for `objects/` writes (integrity belongs to
  `verify`, Task 23, but a post-Put check is cheap insurance per the risk
  table).
- **Error mapping:** distinguish "node unreachable" (preflight `Ping` fail →
  `TV-NODE-01`) from "reachable but permission/space denied" (ssh exits with a
  write/permission error → `TV-NODE-02`) and "key not found" on `Get`/`Stat` of
  an object (→ `TV-OBJ-01`). Inspect exit status / stderr to classify.
- **Remote helpers:** assume a POSIX shell with `cat`, `dd`/`tee`, `stat`/`wc`,
  `sha256sum`, `mkdir -p`, `mv`, `rm`, `ls`/`find` — all standard on the Pi.
- Reuse a `Runner`-style exec seam (mirror `internal/tailscale`) so the SSH
  backend is unit-testable, but the *primary* test target is the stub below
  (real ssh is integration-only).

### internal/backend/stub.go

- **File:** `internal/backend/stub.go`
- **Action:** create
- **Purpose:** a loopback backend over a temp dir implementing `Backend`, used
  as the reusable test double for all downstream engine tests.

```go
// FSBackend stores keys as files under Root (a temp dir). Same semantics as SSH:
// Put dedups on Stat, Get of a missing key -> TV-OBJ-01.
type FSBackend struct{ Root string }
// implements all five methods with os.* calls
```

### internal/backend/contract_test.go

- **File:** `internal/backend/contract_test.go`
- **Action:** create
- **Purpose:** a shared contract-test function any backend can be run through.

```go
// RunContract exercises Put/Stat/Get/Delete/List + dedup + missing-key semantics
// against any Backend, so SSH and FSBackend are verified identically.
func RunContract(t *testing.T, b Backend) { /* … */ }
```

Implementation Notes:

- Export `RunContract` so future backends (Taildrive, Task 22) reuse it.
- The SSH backend is run through `RunContract` only behind a build tag /
  `TAILVAULT_SSH_NODE` env guard (integration); the `FSBackend` runs it always.

## Implementation Checklist

- [ ] `Backend` interface with the exact five signatures + `Meta`.
- [ ] SSH impl: `Stat`, `Get`, `Put` (dedup), `Delete`, `List`.
- [ ] Preflight `Ping` → `TV-NODE-01`; unwritable → `TV-NODE-02`; missing key →
  `TV-OBJ-01`.
- [ ] Temp-to-`mv` atomic writes for object blobs.
- [ ] `FSBackend` loopback implementing the same interface + semantics.
- [ ] Exported `RunContract` shared contract test.

## Testing Requirements

`internal/backend/*_test.go`:

- **Contract (FSBackend):** `RunContract` covers Put→Stat→Get round-trip, List
  by prefix, Delete.
- **Dedup:** `Put` a key, then `Put` the same key again → underlying transfer /
  write happens once (assert via a write counter or mtime unchanged).
- **Get round-trip:** bytes written via `Put` come back identical via `Get`.
- **Missing key:** `Get`/`Stat` of an absent key → `*tserr.Error` code
  `TV-OBJ-01`.
- **SSH error mapping (unit, faked runner):** `Ping` failure → `TV-NODE-01`;
  simulated permission-denied stderr on `Put` → `TV-NODE-02`.

Fixtures: temp dirs via `t.TempDir()`; faked exec runner for the SSH unit tests.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes (FSBackend contract runs without a real node).
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- `Backend` matches the proposal's signatures exactly.
- `FSBackend` passes `RunContract`; `Put` of an already-present key transfers
  zero bytes.
- Missing-key `Get`/`Stat` returns `TV-OBJ-01`; preflight failure returns
  `TV-NODE-01`; unwritable base path returns `TV-NODE-02`.
- `RunContract` is reusable by the future Taildrive backend.

## Related Proposal Sections

> ```go
> type Backend interface {
>     Stat(ctx, key string) (Meta, error)
>     Get(ctx, key string, w io.Writer) error
>     Put(ctx, key string, r io.Reader) error // content-addressed: skip if Stat hits
>     Delete(ctx, key string) error
>     List(ctx, prefix string) ([]string, error)
> }
> // ssh: stream over `ssh user@node` (cat / dd / sha256sum remote-side helpers …).
> ```

> **Storage layout on the node:** `objects/<sha256>` … `refs/<path-id>` …
> `meta/manifest.json`.

> **Push flow:** TV->N: Stat objects/sha … alt missing: TV->N: Put objects/sha.

## Notes & Considerations

- **Gotcha:** preflight must run *before* any byte moves, or a node that drops
  mid-`Put` could leave a partial object — temp-to-`mv` plus preflight are both
  required.
- **Gotcha:** keep `Get` streaming; never read a 1 GB blob fully into memory.
- **For Next Task:** Task 10 (`locations`) constructs an `SSH` backend from a
  `locations.toml` entry and uses `Stat`/`Ping` for reachability in
  `location ls`.
- **Prev:** [task-08-tailscale-wrapper](./task-08-tailscale-wrapper.md) ·
  **Next:** [task-10-locations-registry](./task-10-locations-registry.md)
