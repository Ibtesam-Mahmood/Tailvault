# Task 41: `vault ls` + `vault stat` — Browse the Logical Federated Tree

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Resolution & reachability", "File identity — genesis-hash IDs (dual addressing)", "CLI surface (v2 additions)" · **Block:** 4 — Remote interaction CLI · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Task 27 (SPEC v2: catalog/cache formats, TV-FED codes), Task 28 (`internal/catalog`), Task 30 (`internal/identity` short IDs), Task 31 (`internal/fed` roster + client caches), Task 32 (resolution engine fan-out), Task 40 (`HashObject`) · **Type:** Implementation

## Summary

`vault ls` and `vault stat` are the read surface of the federation: from any
node, with no repo checkout, browse the **logical tree** (`<location>/<relative
path>`) assembled by fanning out to every roster member and merging what each
reports. `ls` lists directory levels of the logical namespace; `stat` shows one
file in full: file ID (12-hex short form, full on `--long`), logical path, sync
mode (`git | manual`), size, current sha256, timestamps, and `last_scanned`
freshness for manual files.

Both commands are **read-only**: they ride tailnet ACL + Tailscale SSH alone —
no password is ever requested (D9 gates mutations only). Per D27, their scope is
**all members**, but there is no global online requirement: partial views are
first-class. Every listing carries **reachability metadata** — which members
answered, which did not — and for offline members the client state caches (D26,
`~/.tailvault/cache/fed-<id>/`) supply advisory last-known entries rendered with
an explicit `last seen <when>` marker, never presented as live truth.

The commands themselves are thin: the heavy lifting (fan-out, cache merge,
partial-view accounting) lives in Block 3's resolution engine (Task 32) and
`internal/fed` (Task 31). This task delivers the Cobra wiring, the tree/table
rendering, and the command-boundary error wrapping per SPEC §8.

## Context

### Related packages

- `cmd/tailvault` — **created here:** `vault ls` / `vault stat` subcommands
  under the `vault` group.
- `internal/resolve` (Task 32) — fan-out query, merged federated view,
  reachability accounting, TV-FED partial-view semantics.
- `internal/fed` (Task 31) — roster, client caches (last-seen snapshots).
- `internal/identity` (Task 30) — `ShortID` (first 12 hex) display form.
- `internal/catalog` (Task 28) — per-member catalog summaries.
- `internal/backend` (Task 40) — `HashObject` for `stat --check` freshness.

### Prerequisites

- [ ] Tasks 27–32 merged (resolution engine returns a merged view + per-member
  reachability).
- [ ] Task 40 merged (`HashObject`).
- [ ] Confirm SPEC v2's cache snapshot fields include a per-member `seen_at`
  timestamp (needed for "last seen <when>").

## Changes Required

### cmd/tailvault/vault_ls.go

- **File:** `cmd/tailvault/vault_ls.go`
- **Action:** create
- **Purpose:** `tailvault vault ls [<location>[/<path>]]` — list the logical
  tree, rooted at the federation, a location, or a subfolder.

```go
// vault ls                      -> top level: one row per member location
// vault ls home-pi/media        -> entries under that logical folder
// flags: --long (full IDs + sha), --ids-only, --json
func runVaultLs(cmd *cobra.Command, args []string) error {
	// view, reach, err := resolve.Snapshot(ctx, fedID)  // fan-out all members
	// merge cache entries for unreachable members (marked stale, seen_at)
	// render tree/table; footer: "N/M members answered; X offline (cached)"
}
```

Implementation Notes:

- Top-level rows show member name, reachability (`online` / `offline — last
  seen 2026-06-10 18:22 UTC` from the cache), file count, total size.
- Entry rows: short ID, sync mode, size (binary units per SPEC §7 formatting),
  relative path. `--long` adds full ID + current sha256 + `last_scanned`.
- An offline member with **no** cache snapshot renders as `offline — no cached
  state` (the "never seen" vs "was here" distinction is exactly what D26 caches
  buy).
- `ls` of a path that resolves to nothing while ≥1 member is unreachable must
  surface the TV-FED partial-view error from Task 32 ("cannot prove absence",
  exit 6) — never an empty listing pretending to be authoritative.

### cmd/tailvault/vault_stat.go

- **File:** `cmd/tailvault/vault_stat.go`
- **Action:** create
- **Purpose:** `tailvault vault stat <logical-path | id>` — one file in full.

```go
// Accepts a logical path ("home-pi/media/a.pdf") or an ID prefix (>=12 hex).
// flags: --check (run HashObject on the home node and compare to catalog),
//        --json
func runVaultStat(cmd *cobra.Command, args []string) error {
	// rec, loc, reach, err := resolve.Lookup(ctx, fedID, target)
	// print: id (short + full), logical path, home, sync_mode, size, sha256,
	//        ingested_at, last_scanned, genesis summary, reachability footer
}
```

Implementation Notes:

- ID-prefix lookup is unambiguous-prefix matching (like git): ambiguous prefix
  → error listing candidates.
- For `sync_mode = manual` files, print hash **freshness**: `sha256 as of last
  scan <last_scanned>` — manual files are editable in place, so the catalog sha
  may legitimately trail disk (H12). `--check` calls `HashObject` against the
  home node and reports `fresh` / `drifted since last scan` without ever
  labelling drift "corrupt" (that distinction belongs to verify, Task 38).
- Found at a member other than the recorded home → success + WARN ("run
  `tailvault heal`"), per the frozen resolution semantics.
- SPEC §8 layering: `resolve`/`fed`/`catalog` return their typed/plain errors;
  this command maps them at the boundary (`TV-FED-*` → exit 6, `TV-OBJ` →
  exit 5, etc. via `tserr.ExitCodeFor`).

## Implementation Checklist

- [ ] `vault` Cobra group exists (create if Task 33 has not already).
- [ ] `vault ls` with `--long`, `--ids-only`, `--json`; reachability footer.
- [ ] Cache-backed `last seen <when>` rows for offline members; `no cached
  state` for never-seen.
- [ ] `vault stat` by logical path and by unambiguous ID prefix.
- [ ] Manual-file freshness display + `--check` via `HashObject`.
- [ ] No password prompt on any code path (reads are exempt).
- [ ] TV-FED partial-view surfaced, never an authoritative-looking empty result.

## Testing Requirements

`cmd/tailvault/*_test.go` against the Task 39 multi-node harness (stub backends
only):

- **Happy path:** 3 members, all up → `ls` shows all entries, footer `3/3`.
- **Partial view:** 1 member down with cache → its entries render `last seen`;
  down without cache → `no cached state`; lookups that miss → TV-FED, exit 6.
- **stat by ID:** full ID, 12-hex short, ambiguous prefix (error), unknown ID.
- **Moved file:** found at non-home member → success + heal WARN.
- **Manual freshness:** drift the stub file's bytes after catalog write →
  `--check` reports `drifted since last scan`.
- **Golden output:** `--json` shape is asserted (it is the scriptable contract).

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- `vault ls` / `vault stat` work from a clean machine with only
  `locations.toml` + cache state — no repo checkout, no password.
- Every output carries reachability metadata; offline members show cached
  last-seen, clearly marked stale.
- IDs display as 12-hex short form by default; prefix lookup works.
- Partial-view miss → `TV-FED-*` hard-fail, exit 6; all-members miss →
  TV-OBJ missing, exit 5.

## Related Proposal Sections

> **Remote interaction from any node** — browse, read metadata, download,
> ingest, move, and manage sync modes via the CLI, no repo checkout needed.

> **Client state caches** (advisory, never authoritative) … used to distinguish
> "was here, now offline" from "never existed", to show last-known state …
> Live pings always win.

> Every remote view carries reachability metadata.

## Notes & Considerations

- **Gotcha:** never merge cached entries into the live set silently — stale rows
  must be visually distinct, or a user will `rm` based on a ghost.
- **Gotcha:** `ls` fan-out is the all-members scope (D27) but must not hang on
  one slow member — bounded per-member timeout, report as unreachable.
- **For Next Task:** Task 42 (`vault get`) reuses `resolve.Lookup` and the
  path-or-ID target parsing introduced here — factor target parsing into a
  shared helper.
- **Prev:** [task-40-remote-hash-shortcircuit](./task-40-remote-hash-shortcircuit.md) ·
  **Next:** [task-42-vault-get-receipts](./task-42-vault-get-receipts.md)
