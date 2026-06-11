# Task 07: Structured Error Model

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Detailed Design → "Error model — fail clearly when the node isn't reachable"; Phase 2 → "Structured error model" · **Block:** 1 — MVP · **Estimated Effort:** 0.5 ideal eng-day · **Dependencies:** Task 02 (Go module + Cobra CLI skeleton — gives `main` to wire the exit-code converter into) · **Type:** Foundation

## Summary

Hard-fail is a core guarantee of tailvault, and the failure must be *legible*.
Every command that needs the storage node preflights reachability and aborts
before any partial work; when it aborts, it must do so with a stable, typed
error rather than a raw SSH transcript or Go stack trace. This task defines
`internal/tserr`: a small set of typed conditions, each with a stable code, a
one-line cause, and a concrete fix, plus a bucketed exit-code mapping so scripts
and the git `pre-push` hook can branch on *why* a command failed.

The package exposes an `Error` type carrying `Code`, `Cause`, and `Fix`,
formatting as `TV-…: <cause> (fix: <fix>)`, and an `ExitCode()` that maps each
code into one of the proposal's exit-code buckets. A helper in `main` converts a
returned `*Error` into the process exit code so a failed push surfaces an
obvious code rather than a generic git error.

This is a Foundation package every later command depends on: the Tailscale
wrapper (Task 08), the backends (Task 09), and `push`/`pull`/`verify` all return
`tserr` codes. Keeping the codes and buckets correct here is what makes the
whole tool's failure behavior predictable.

## Context

### Related packages

- `internal/tserr` — **created here.** Codes, `Error` type, exit-code map.
- `internal/tailscale` (Task 08) — returns `TV-NET-01` / `TV-NET-02`.
- `internal/backend` (Task 09) — returns `TV-NODE-01` / `TV-NODE-02` /
  `TV-OBJ-01`.
- `cmd/tailvault/main.go` (Task 02) — calls the exit-code converter.

### Prerequisites

- [ ] Task 02 merged: `cmd/tailvault/main.go` exists and owns process exit.
- [ ] Confirm the five codes and five buckets from the proposal (below).

## Changes Required

### internal/tserr/tserr.go

- **File:** `internal/tserr/tserr.go`
- **Action:** create
- **Purpose:** define stable codes, the `Error` type, formatting, and the
  bucketed `ExitCode()` mapping.

```go
package tserr

import "fmt"

// Code is a stable, documented error identifier surfaced to users and scripts.
type Code string

const (
	NetNotRunning  Code = "TV-NET-01"  // Tailscale not running / not in PATH
	NetNotLoggedIn Code = "TV-NET-02"  // not logged into the tailnet
	NodeOffline    Code = "TV-NODE-01" // storage node offline/unreachable
	NodeNotWritable Code = "TV-NODE-02" // node reachable but base_path not writable
	ObjMissing     Code = "TV-OBJ-01"  // expected blob missing on the node
)

// Error is a typed tailvault failure: stable code, one-line cause, concrete fix.
type Error struct {
	Code  Code
	Cause string
	Fix   string
	Err   error // optional wrapped underlying error (for %w / debugging)
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s (fix: %s)", e.Code, e.Cause, e.Fix)
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCode maps a code into the proposal's bucketed process exit codes:
//   0 success, 2 config/precondition, 3 network/Tailscale,
//   4 node unreachable, 5 integrity/missing blob.
func (e *Error) ExitCode() int {
	switch e.Code {
	case NetNotRunning, NetNotLoggedIn:
		return 3
	case NodeOffline, NodeNotWritable:
		return 4
	case ObjMissing:
		return 5
	default:
		return 2 // config/precondition fallback
	}
}

// Helper constructors keep call sites terse and the cause/fix text consistent.
func NetNotRunningErr(err error) *Error { /* preset cause + fix */ }
// … one per code (NetNotLoggedInErr, NodeOfflineErr(node), NodeNotWritableErr(node), ObjMissingErr(sha)) …
```

Implementation Notes:

- Provide one constructor per code with the proposal's canonical cause/fix text
  preset, taking only the variable bits (e.g. node name, sha). This avoids
  drift in user-facing strings across packages.
- The default bucket is `2` (config/precondition) so any unmapped/new code fails
  safe as a precondition error rather than masquerading as success.
- `ExitCode` is a method on `*Error`; a free function `ExitCodeFor(err error)
  int` (below) handles the `main` boundary including non-`*Error` values.

### cmd/tailvault/main.go

- **File:** `cmd/tailvault/main.go`
- **Action:** modify
- **Purpose:** convert a returned error into the process exit code.

```go
// In main(), after Execute() returns err:
os.Exit(tserr.ExitCodeFor(err))

// In internal/tserr:
func ExitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var e *Error
	if errors.As(err, &e) {
		return e.ExitCode()
	}
	return 1 // generic/unexpected failure
}
```

Implementation Notes:

- `errors.As` so a `tserr.Error` wrapped deeper in the chain still maps
  correctly (e.g. wrapped by a command).
- Non-`*Error` failures map to generic exit `1`; only the five typed codes get
  the bucketed `2/3/4/5`. Success is `0`.
- Print `err.Error()` to stderr before exiting so the code *and* the
  `TV-…: cause (fix: …)` line are both visible to the user / hook.

Key Considerations:

- The `pre-push` hook (Task 19) reads this exit code to decide whether to abort
  the push; the bucket numbers are a public contract — do not renumber.

## Implementation Checklist

- [ ] Five `Code` constants with proposal-exact values.
- [ ] `Error{Code, Cause, Fix, Err}` with `Error()` and `Unwrap()`.
- [ ] `ExitCode()` bucket map: NET→3, NODE→4, OBJ→5, default→2.
- [ ] Per-code constructors with canonical cause/fix strings.
- [ ] `ExitCodeFor(error) int` using `errors.As`; nil→0, untyped→1.
- [ ] `main` calls `os.Exit(tserr.ExitCodeFor(err))` and prints to stderr.

## Testing Requirements

`internal/tserr/tserr_test.go` — table-driven:

- **Code → bucket:** each of the five codes maps to its expected exit bucket
  (3,3,4,4,5); an unknown code → 2.
- **`Error()` formatting:** asserts exact string
  `TV-NET-01: Tailscale not running (fix: start Tailscale and run \`tailscale status\`)`
  for a representative case.
- **`ExitCodeFor`:** nil→0; a bare `errors.New("x")`→1; a wrapped
  `fmt.Errorf("…: %w", tserrValue)`→correct bucket via `errors.As`.
- **`Unwrap`:** wrapped underlying error is retrievable.

Fixtures: none; construct errors inline.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- Each of the five codes returns its documented exit bucket.
- `Error()` renders exactly `TV-…: <cause> (fix: <fix>)`.
- `ExitCodeFor` returns 0 for nil, 1 for untyped errors, and the correct bucket
  for typed errors even when wrapped.
- `main` exits with the mapped code and prints the legible error line.

## Related Proposal Sections

> A small set of typed conditions, each with a stable code, a one-line cause,
> and a concrete next step. Examples: `TV-NET-01 — Tailscale not running.` …
> `TV-NET-02 — Not logged into the tailnet.` … `TV-NODE-01 — Storage node
> 'home-pi' is offline/unreachable.` … `TV-NODE-02 — Node reachable but
> base_path not writable.` … `TV-OBJ-01 — Expected blob <sha> missing on the
> node.`

> **Exit codes** are bucketed … `0` success; `2` config/precondition; `3`
> network/Tailscale down; `4` node unreachable; `5` integrity/missing blob. The
> `pre-push` hook surfaces the same code so a failed push reads obviously rather
> than as a generic git error.

## Notes & Considerations

- **Gotcha:** keep cause/fix strings in the constructors, not at call sites, or
  the same code will render different text in different commands.
- **Gotcha:** never reuse exit code `1` for a typed error — it's the catch-all
  for unexpected failures and the hook treats the buckets specially.
- **For Next Task:** Task 08 returns `TV-NET-01`/`TV-NET-02`; Task 09 returns
  the `TV-NODE-*`/`TV-OBJ-01` codes. Both import this package.
- **Prev:** [task-06-pointer-format](./task-06-pointer-format.md) ·
  **Next:** [task-08-tailscale-wrapper](./task-08-tailscale-wrapper.md)
