# Task 08: Tailscale CLI Wrapper

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Detailed Design → "Tailscale integration points" and "Node discovery & location registration (reads the local session)"; "Error model" · **Block:** 1 — MVP · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Task 02 (CLI skeleton + module layout), Task 07 (`internal/tserr` for `TV-NET-01`/`TV-NET-02`) · **Type:** Implementation

## Summary

tailvault deliberately carries almost no networking code: addressing, liveness,
and identity all ride on Tailscale primitives. This task wraps the local
`tailscale` CLI as a small Go package — `internal/tailscale` — shelling out to
the binary and parsing its output. It reads **only the local, already
authenticated session**; it performs no login and stores no credentials.

Three operations are needed by the rest of the tool: `Status()` parses
`tailscale status --json` into the set of peers (MagicDNS name + online state)
used for the setup pick-list and reachability checks; `Ping(node)` confirms a
specific node is reachable for preflight hard-fail; and `Whois(addr)` resolves a
tailnet identity for the `pusher` stamp written into `tailvault.lock`. Absent
binary, logged-out, or daemon-down conditions map to the structured
`tserr` codes so failures are legible.

This is the liveness/identity foundation under the backend layer (Task 09),
`location ls` (Task 10), interactive `setup` (Task 11), and the `push` pusher
stamp. It contains no business logic — just faithful shelling-out and parsing,
with errors translated into tailvault's typed model.

## Context

### Related packages

- `internal/tailscale` — **created here.**
- `internal/tserr` (Task 07) — `TV-NET-01` (not running) / `TV-NET-02` (logged
  out) are returned from `Status`.
- `internal/backend` (Task 09) — calls `Ping` in preflight.
- `internal/locations` (Task 10) — `location ls` uses `Status`/`Ping`.

### Prerequisites

- [ ] Task 02 merged (module layout, CLI skeleton).
- [ ] Task 07 merged (`tserr` codes available).
- [ ] A captured `tailscale status --json` sample to fixture against (snippet
  below).

## Changes Required

### internal/tailscale/tailscale.go

- **File:** `internal/tailscale/tailscale.go`
- **Action:** create
- **Purpose:** typed wrappers over `tailscale status --json`, `ping`, `whois`.

```go
package tailscale

import (
	"context"
	"encoding/json"
	"os/exec"
)

// Peer is the subset of a tailscale status peer tailvault cares about.
type Peer struct {
	DNSName string // MagicDNS name, e.g. "home-pi.tailnet-name.ts.net."
	Online  bool
}

// Status is the parsed, trimmed view of `tailscale status --json`.
type Status struct {
	Self     Peer
	Peers    []Peer
	LoggedIn bool
}

// Runner indirects exec so tests inject canned output. Default shells to PATH.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

type Client struct{ R Runner }

// Status parses `tailscale status --json`.
// Maps a missing binary / unreachable daemon -> TV-NET-01,
// and a logged-out session (BackendState != "Running") -> TV-NET-02.
func (c *Client) Status(ctx context.Context) (Status, error)

// Ping shells `tailscale ping --c 1 <node>`; non-zero/timeout -> caller maps to TV-NODE-01.
func (c *Client) Ping(ctx context.Context, node string) error

// Whois shells `tailscale whois --json <addr>` -> "user@host" identity for the pusher stamp.
func (c *Client) Whois(ctx context.Context, addr string) (string, error)
```

Implementation Notes:

- **Binary detection:** if `exec.LookPath("tailscale")` fails *or* running it
  yields `exec.ErrNotFound` / a "command not found"-style error, return
  `tserr.NetNotRunningErr` (`TV-NET-01`). A connection error to `tailscaled`
  (the daemon down) also maps to `TV-NET-01`.
- **Logged-out detection:** `tailscale status --json` reports a top-level
  `BackendState`. When it is `NeedsLogin` / `Stopped` / `NoState` (anything but
  `Running`), set `LoggedIn=false` and return `tserr.NetNotLoggedInErr`
  (`TV-NET-02`). Parse, don't guess.
- **Peer model:** decode only the fields needed (`Self`, `Peer` map → each
  entry's `DNSName` and `Online`). Use a private struct mirroring the JSON and
  project it into the trimmed public `Status`/`Peer`. Trim the trailing `.` on
  MagicDNS names so callers get `home-pi.tailnet.ts.net`.
- **`Runner` seam:** the default `execRunner` calls `exec.CommandContext`; tests
  supply a `fakeRunner` returning fixture bytes + a chosen error. This is what
  makes the package unit-testable with no real Tailscale present.
- **No login, ever:** never invoke `tailscale up`/`login`; this package is
  strictly read-only of the local session per the proposal's non-goals.

Captured `tailscale status --json` fixture (trim to the parsed fields; store as
a test fixture string):

```json
{
  "BackendState": "Running",
  "Self": { "DNSName": "laptop.tailnet-name.ts.net.", "Online": true },
  "Peer": {
    "nodekey:abc123": { "DNSName": "home-pi.tailnet-name.ts.net.", "Online": true },
    "nodekey:def456": { "DNSName": "office-nas.tailnet-name.ts.net.", "Online": false }
  }
}
```

Key Considerations:

- Output schema varies across Tailscale versions; decode defensively (only the
  fields above) and ignore unknown keys (default `encoding/json` behavior).
- `Ping`/`Whois` failures are *not* mapped to `TV-NET-*` here — node-level
  mapping (`TV-NODE-01`) is the backend/preflight's job (Task 09). Return a
  plain error or surface stderr so the caller decides.

## Implementation Checklist

- [ ] `Peer`, `Status` trimmed types; `Runner` interface + default exec runner.
- [ ] `Status()` parses `--json`, projects Self + Peers, sets `LoggedIn`.
- [ ] Missing binary / daemon down → `TV-NET-01`.
- [ ] `BackendState != "Running"` → `TV-NET-02`.
- [ ] MagicDNS trailing-dot trim.
- [ ] `Ping(node)` and `Whois(addr)` shelling out via the `Runner`.
- [ ] No login/credential code anywhere.

## Testing Requirements

`internal/tailscale/tailscale_test.go` — table-driven with a `fakeRunner`:

- **Parse fixture:** the captured `--json` snippet decodes to Self =
  `laptop.tailnet-name.ts.net`, two peers, `home-pi` online / `office-nas`
  offline, `LoggedIn=true`.
- **Absent binary:** `fakeRunner` returns `exec.ErrNotFound` → `Status` returns
  a `*tserr.Error` with code `TV-NET-01`.
- **Logged out:** fixture with `"BackendState":"NeedsLogin"` → `TV-NET-02`.
- **MagicDNS trim:** trailing `.` removed from all returned `DNSName`s.
- **Whois:** fixture whois JSON → expected `user@host` string.

Fixtures: inline JSON strings (the snippet above + a logged-out variant).

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes (no real `tailscale` binary required).
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- `Status` parses the captured fixture into the trimmed model with correct
  online flags and MagicDNS names.
- Missing binary → `TV-NET-01`; logged-out session → `TV-NET-02`, both as typed
  `tserr.Error`s.
- Tests run with no Tailscale installed (the `Runner` seam is exercised).
- No code path logs in, authenticates to the control plane, or stores
  credentials.

## Related Proposal Sections

> tailvault reads the **local, already authenticated Tailscale session** via
> `tailscale status --json` and offers the tailnet's nodes as a pick-list …
> **No Tailscale login or API token is involved.**

> Per `DESIGN.md` §5: MagicDNS/IP for addressing, Tailscale SSH for the SSH
> backend, … `tailscale status`/`ping` for liveness + hard-fail, … `tailscale
> whois` for the pusher stamp.

> `TV-NET-01 — Tailscale not running.` *Cause:* `tailscaled` not reachable /
> `tailscale` not in PATH. … `TV-NET-02 — Not logged into the tailnet.`

## Notes & Considerations

- **Gotcha:** the `--json` schema is large and version-dependent — decode only
  the few fields tailvault needs, never the whole document.
- **Gotcha:** distinguish "daemon down" (TV-NET-01) from "logged out"
  (TV-NET-02) by inspecting `BackendState`, not by string-matching stderr.
- **For Next Task:** Task 09's SSH backend and preflight call `Ping`; Task 10's
  `location ls` calls `Status`/`Ping`; `push` calls `Whois` for the stamp.
- **Prev:** [task-07-error-model](./task-07-error-model.md) ·
  **Next:** [task-09-backend-ssh](./task-09-backend-ssh.md)
