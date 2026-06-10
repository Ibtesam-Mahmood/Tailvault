# Task 10: Locations Registry

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Detailed Design → "User-level registry — `~/.config/tailvault/locations.toml` (NOT in repo)"; CLI surface → `location add` / `location ls`; Goals (registry kept out of the repo) · **Block:** 1 — MVP · **Estimated Effort:** 1.5 ideal eng-days · **Dependencies:** Task 08 (`internal/tailscale` for `Ping`), Task 07 (`internal/tserr`); also consumes Task 09's `Backend.Stat` for reachability · **Type:** Implementation

## Summary

Storage locations are user-level, not repo-level: the registry of "which
Tailscale node, what base path, which backend" lives at
`~/.config/tailvault/locations.toml` and is deliberately kept **out** of the
repo (the repo's `tailvault.toml` only references a location *by name*). This
task implements `internal/locations`: read/write that file plus the
`tailvault location add` and `tailvault location ls` commands.

`location add <name>` writes an entry from manual flags (`--node`,
`--base-path`, `--backend ssh|taildrive`, `--user`, `--share`). `location ls`
lists registered entries annotated with **live reachability** — resolved via
`tailscale.Ping` and/or a backend `Stat` so the user can see at a glance which
nodes are up. The interactive pick-list (enumerating tailnet peers) is the
*next* task (Task 11); this task delivers the manual path and the registry
plumbing it will build on.

The registry format mirrors the proposal exactly (`[locations.<name>]` tables
with `node`, `base_path`, `backend`, and `user`/`share`). Round-tripping it
losslessly and reporting reachability honestly are the two things that matter
here.

## Context

### Related packages

- `internal/locations` — **created here.** Registry read/write + reachability.
- `cmd/tailvault/location.go` — **created here.** `location add` / `location ls`
  Cobra commands.
- `internal/tailscale` (Task 08) — `Ping` / `Status` for reachability.
- `internal/backend` (Task 09) — `Stat` as a deeper reachability probe; backend
  selection by `backend` field.
- `internal/tserr` (Task 07) — error mapping.

### Prerequisites

- [ ] Tasks 07, 08, 09 merged.
- [ ] `pelletier/go-toml/v2` available as a module dependency.
- [ ] Confirm the `locations.toml` schema (below).

## Changes Required

### internal/locations/locations.go

- **File:** `internal/locations/locations.go`
- **Action:** create
- **Purpose:** typed registry model + load/save against
  `~/.config/tailvault/locations.toml`.

Schema (the proposal's registry block):

```toml
[locations.home-pi]
node      = "home-pi.tailnet-name.ts.net"
base_path = "/mnt/ssd/tailvault"
backend   = "ssh"
user      = "ibte"

[locations.office-nas]
node      = "100.92.14.7"
base_path = "/vault"
backend   = "taildrive"
share     = "vault"
```

```go
package locations

type Backend string // "ssh" | "taildrive"

type Location struct {
	Node     string `toml:"node"`
	BasePath string `toml:"base_path"`
	Backend  Backend `toml:"backend"`
	User     string `toml:"user,omitempty"`  // ssh
	Share    string `toml:"share,omitempty"` // taildrive
}

type Registry struct {
	Locations map[string]Location `toml:"locations"`
}

// Path returns the registry path, honoring XDG_CONFIG_HOME, default
// ~/.config/tailvault/locations.toml.
func Path() (string, error)

// Load reads the registry; a missing file yields an empty Registry, not an error.
func Load() (Registry, error)

// Save writes the registry, creating ~/.config/tailvault/ (0700) if needed.
func (r Registry) Save() error

// Add inserts/updates a named entry, validating required fields per backend.
func (r *Registry) Add(name string, loc Location) error
```

Implementation Notes:

- **Out of repo:** the path is under `~/.config` (respect `XDG_CONFIG_HOME`),
  never inside the working tree — this enforces the Goal "registry kept out of
  the repo."
- **Missing file is not an error:** `Load` returns an empty `Registry{}` so the
  first `location add` works on a fresh machine.
- **Validation:** `ssh` requires `node`, `base_path`, `user`; `taildrive`
  requires `node`, `base_path`, `share`. A bad/empty `backend` or missing
  required field → `tserr` config error (exit bucket 2).
- Directory perms `0700`, file perms `0600` (it names internal infra paths).

### internal/locations/reachable.go

- **File:** `internal/locations/reachable.go`
- **Action:** create
- **Purpose:** compute live reachability for `location ls`.

```go
type Reachability struct {
	Name      string
	Reachable bool
	Detail    string // e.g. "online", "ping failed", "base_path not writable"
}

// Check pings the node (and optionally Stats the backend) to classify liveness.
// Pinger/Stater are injected so tests can stub them.
func Check(ctx context.Context, name string, loc Location,
	ping func(ctx context.Context, node string) error) Reachability
```

Implementation Notes:

- Minimum viable probe: `ping(loc.Node)`; reachable iff ping succeeds. Optionally
  deepen with a backend `Stat("objects/")` to catch `TV-NODE-02`
  (reachable-but-unwritable) and surface that in `Detail`.
- Inject the ping/stat funcs so `location ls` tests run with a stub (no real
  tailnet) — mirrors the `Runner` seam pattern from Tasks 08/09.

### cmd/tailvault/location.go

- **File:** `cmd/tailvault/location.go`
- **Action:** create
- **Purpose:** `location add` and `location ls` Cobra commands.

```go
// tailvault location add <name> --node --base-path --backend ssh|taildrive --user --share
//   builds a Location from flags, validates, r.Add, r.Save.
// tailvault location ls
//   Load registry, Check each entry, print a table: NAME  NODE  BACKEND  REACHABLE
```

Implementation Notes:

- `location add` is **manual** here (flag-driven). Task 11 adds the interactive
  pick-list that prefills `--node` from the tailnet; structure `add` so that
  path can call into the same `Registry.Add`.
- `location ls` output: one row per entry with a clear reachable/unreachable
  marker and the `Detail` string; non-zero exit only on a registry read error,
  not on an unreachable node (ls is informational).

## Implementation Checklist

- [ ] `Location` / `Registry` types matching the TOML schema.
- [ ] `Path()` honoring `XDG_CONFIG_HOME`, default `~/.config/tailvault/`.
- [ ] `Load` (missing file → empty), `Save` (0700 dir / 0600 file), `Add`
  (per-backend validation).
- [ ] `Check` reachability via injected ping(/stat).
- [ ] `location add` command (manual flags) → write entry.
- [ ] `location ls` command → table with live reachability.

## Testing Requirements

`internal/locations/*_test.go`:

- **Registry round-trip:** Save a `Registry` with an ssh and a taildrive entry
  to a temp `XDG_CONFIG_HOME`, `Load` it back, assert equality.
- **Missing file:** `Load` on a non-existent path → empty registry, no error.
- **Validation:** `Add` with `backend=ssh` and no `user` → config error; bad
  backend string → error.
- **`Check` reachable/unreachable:** stub ping returning nil → `Reachable=true`;
  stub returning a `TV-NODE-01` error → `Reachable=false` with detail.

Fixtures: `t.TempDir()` + `t.Setenv("XDG_CONFIG_HOME", …)`; stub ping func.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- A registry with ssh + taildrive entries round-trips losslessly through
  `Save`/`Load`.
- The file lives under `~/.config/tailvault/` (or `XDG_CONFIG_HOME`), never in
  the repo.
- `location add <name>` with valid flags writes a correct entry; invalid flag
  combos fail with a config-bucket error.
- `location ls` marks each entry reachable/unreachable using the injected
  ping/stat, verifiable with a stub.

## Related Proposal Sections

> **User-level registry — `~/.config/tailvault/locations.toml` (NOT in repo)**
> ```toml
> [locations.home-pi]
> node = "home-pi.tailnet-name.ts.net"
> base_path = "/mnt/ssd/tailvault"
> backend = "ssh"
> user = "ibte"
> ```

> `tailvault location add <name>` — register a tailnode target (writes
> locations.toml); `--node` to set manually … `tailvault location ls` — list
> registered locations + live reachability.

> **Goals:** … user-level registry of storage locations kept **out** of the repo.

## Notes & Considerations

- **Gotcha:** never write the registry inside the working tree — the
  out-of-repo placement is a stated Goal and a privacy boundary (it names
  internal infra).
- **Gotcha:** `location ls` must not exit non-zero just because a node is down;
  unreachability is data it reports, not a command failure.
- **For Next Task:** Task 11 (interactive `setup` / `location add`) enumerates
  tailnet peers via `tailscale.Status` and prefills `--node`, then reuses this
  task's `Registry.Add`/`Save`.
- **Prev:** [task-09-backend-ssh](./task-09-backend-ssh.md) ·
  **Next:** [task-11-interactive-setup](./task-11-interactive-setup.md)
