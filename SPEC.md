# SPEC.md — Tailvault frozen contract (v1)

> **Normative.** This document is the single frozen reference for the four file
> schemas, the structured error catalogue, and the ten resolved Open Questions.
> Implementation tasks (02–25) cite this file when they say "mirror the config
> block" or "use the canonical lock ordering". It is internally consistent with
> [`proposal.md`](./proposal.md) and [`DESIGN.md`](./DESIGN.md); where a later
> change would diverge, a follow-up issue reconciles all three — do not silently
> diverge.

Sample blocks below are quoted verbatim from `proposal.md` so implementers can
paste them into test fixtures.

---

## 1. `tailvault.toml` — repo-committed project config

Committed into the repo so every clone agrees on what is vault-managed and where
it lives. The location *name* is resolved at runtime against the user-level
[`locations.toml`](#4-locationstoml--user-level-registry-not-in-repo) (never
committed), so the repo carries no node addresses or credentials.

| Field | Type | Default | Validation |
|---|---|---|---|
| `version` | int | `1` | MUST equal `1`; otherwise config error (`TV-CFG`, exit 2) |
| `[storage].location` | string | — (required) | non-empty; resolved against `locations.toml` at runtime |
| `[storage].subpath` | string | `""` | optional child folder under the location's `base_path` |
| `[rules].min_size` | string | `"5MB"` | human size string; parses to bytes — see [§6 Q5](#6-resolved-open-questions) and [the size-unit binding](#7-size-unit-binding-minsize) |
| `[rules].include` | []string | `[]` | doublestar globs (`**/*.pdf`, …) |
| `[rules].exclude` | []string | `[]` | doublestar globs; **exclude wins over include** |
| `[rules].history` | bool | `false` | global history default (anti-bloat) |
| `[rules].auto_delete` | bool | `true` | prune the blob when the file is deleted from git |
| `[[rules.overrides]].match` | string | — (required in an override) | doublestar glob; **first match wins** |
| `[[rules.overrides]].history` | bool | inherits `[rules].history` | per-pattern override |
| `[[rules.overrides]].preserve` | bool | `false` | when true, never auto-delete |

### Sample (verbatim from proposal.md)

```toml
# tailvault project config — committed to the repo
version = 1

[storage]
location = "home-pi"      # name resolved via ~/.config/tailvault/locations.toml
subpath  = "root-pnp"     # optional child folder under the location's base_path

[rules]
min_size    = "5MB"                       # files >= this are vault-managed
include     = ["**/*.pdf", "**/*.stl", "**/*.3mf", "**/*.pptx"]
exclude     = ["**/*.tmp", "drafts/**"]
history     = false                        # default: no version history (anti-bloat)
auto_delete = true                         # default: prune from storage on git delete

# Per-pattern overrides; first match wins.
[[rules.overrides]]
match    = "masters/**"
history  = true
preserve = true                            # never auto-delete
```

### Rule evaluation order (normative)

A file is **vault-managed** when, in order:
1. it does not match any `exclude` glob (exclude always wins), **and**
2. it matches an `include` glob **or** its size `>= min_size`.

The effective `history` / `preserve` flags for a managed file come from the
**first** matching `[[rules.overrides]].match` (override order in the file is the
match order); if none match, `history` falls back to `[rules].history` and
`preserve` falls back to `false`. `auto_delete` is global and is suppressed for a
file whose effective `preserve` is true.

---

## 2. `tailvault.lock` — repo-committed state (canonical form)

The source of truth for "what is stored, where, and when." Committed so every
clone agrees, and written in a **canonical form** so the per-path union merge
driver (Task 24) produces minimal, conflict-free diffs.

Top-level keys:
- `version = 1`
- `generated_by = "tailvault <ver>"` (e.g. `"tailvault 0.1.0"`).

Each entry is a `[[entry]]` table. **Canonical ordering rules:**
- Entries are sorted by `path`, **byte-wise ascending**, stable across writes.
- Field order **within** each entry is fixed:
  `path`, `sha256`, `size`, `location`, `pushed_at`, `pusher`, `history`,
  `preserve`, `deleted` (tombstones only), then `versions` (history-on entries
  only).
- `deleted = true` marks a **tombstone**: the working file is gone but its blob
  must survive — emitted only when the path was preserved or `auto_delete` was
  opted out. `push` retains tombstone entries instead of dropping them so the
  sha stays in the GC keep-set; omitted for live entries (default `false`). In a
  merge, **live beats tombstone** for the same path+sha: a path is `deleted` in
  the merged lock only if *both* sides are tombstones, so a file still live on
  any branch is materialized by `pull` rather than silently dropped.
- `versions = ["<newest>", …, "<oldest>"]` — **newest-first** (load-bearing for
  `revert` (Task 21) and GC keep-set construction (Task 16); the direction is
  normative).
- `pushed_at` is **RFC3339 UTC** with a `Z` suffix, e.g. `2026-06-10T18:22:04Z`.
- `pusher` comes from `tailscale whois`, falling back to git `user.email`
  ([§6 Q7](#6-resolved-open-questions)).

### Entry fields

| Field | Type | Notes |
|---|---|---|
| `path` | string | logical repo-relative path; the sort key |
| `sha256` | string | hex; current content → `objects/<sha256>` |
| `size` | int | bytes |
| `location` | string | location name (resolved via `locations.toml`) |
| `pushed_at` | string | RFC3339 UTC, `Z`-suffixed |
| `pusher` | string | `tailscale whois`, else git `user.email` |
| `history` | bool | effective history flag for this entry |
| `preserve` | bool | effective preserve flag for this entry |
| `deleted` | bool | **tombstones only**; working file gone, blob retained |
| `versions` | []string | **history-on only**; prior shas, newest-first |

### Sample (verbatim from proposal.md)

```toml
version = 1
generated_by = "tailvault 0.1.0"

[[entry]]
path      = "pnp/Root - Clockwork Expansion/board.pdf"
sha256    = "9f2b1c…"                       # current content → objects/9f2b1c…
size      = 41231873
location  = "home-pi"
pushed_at = "2026-06-10T18:22:04Z"
pusher    = "ibte@laptop"                   # from `tailscale whois`
history   = false
preserve  = false
# history-on entries additionally carry, newest-first:
# versions = ["9f2b1c…", "7c10aa…"]
```

---

## 3. Pointer file — the in-git stand-in

The `clean` filter replaces a large file's bytes with this on commit; `smudge`
restores the real bytes on checkout. **Exactly four lines, in this order:**

```
tailvault.v1
sha256 <hex>
size <bytes>
location <name>
```

- Line 1 is the literal magic `tailvault.v1`. A parser MUST reject any other
  magic (unknown / future version → config-style error).
- Lines 2–4 are `key SP value` (a single ASCII space separates key and value).
- Keys appear exactly once, in the order `sha256`, `size`, `location`. No
  trailing or unknown keys are permitted.
- A trailing newline after line 4 is allowed; no other whitespace is
  significant.

---

## 4. `locations.toml` — user-level registry (NOT in repo)

Lives at `~/.config/tailvault/locations.toml`. Holds the named storage targets;
never committed (it carries node addresses and, for SSH, the login user).

| Field | Type | Filled by | Notes |
|---|---|---|---|
| `node` | string | discovery or `--node` | MagicDNS name or `100.x` IP |
| `base_path` | string | interactive prompt | e.g. `/mnt/ssd/tailvault` (USB3 SSD, **not** the boot SD) |
| `backend` | string | interactive prompt | `ssh` \| `taildrive` |
| `user` | string | interactive prompt (ssh) | SSH user (ssh backend only) |
| `share` | string | interactive prompt (taildrive) | Taildrive share name (taildrive backend only) |

`node` is prefilled from `tailscale status --json` peer enumeration — the
**local, already-authenticated session only**, no API or login
([§6 Q9](#6-resolved-open-questions)). `base_path`, `backend`, and
`user`/`share` come from interactive prompts. Manual entry (`--node`) is always
available as a fallback when the daemon can't enumerate peers.

### Sample (verbatim from proposal.md)

```toml
[locations.home-pi]
node      = "home-pi.tailnet-name.ts.net"  # MagicDNS or 100.x IP
base_path = "/mnt/ssd/tailvault"            # on a USB3 SSD, not the boot SD
backend   = "ssh"                           # ssh | taildrive
user      = "ibte"

[locations.office-nas]
node      = "100.92.14.7"
base_path = "/vault"
backend   = "taildrive"
share     = "vault"
```

### Storage layout on the node (for reference)

```
<base_path>/<subpath>/
  objects/<sha256>     # content-addressed blobs, deduped
  refs/<path-id>       # history-on files only: newest-first list of shas
  meta/manifest.json   # optional bookkeeping (GC marks, integrity log)
```

---

## 5. Error catalogue

Errors are **structured**: every failure that the user can hit carries a stable
code, a one-line cause, a concrete next step, and maps to a **bucketed exit
code**. Every command that needs the storage node runs a preflight
(`tailscale status` for tailnet health, then `tailscale ping` / a backend
`Stat`) and **aborts before any partial work** if the node isn't reachable, so a
node-down failure leaves no partial upload and an unadvanced lock.

| Code | Cause | Fix | Exit bucket |
|---|---|---|---|
| `TV-NET-01` | `tailscaled` not reachable / `tailscale` not in PATH | start Tailscale; run `tailscale status` | 3 |
| `TV-NET-02` | Not logged into the tailnet | `tailscale up` | 3 |
| `TV-NODE-01` | Storage node offline/unreachable (not in `status`, or `ping`/`Stat` failed) | power on/connect the node; `tailvault location ls` | 4 |
| `TV-NODE-02` | Node reachable but `base_path` not writable | check SSH user / Taildrive share + permissions | 4 |
| `TV-OBJ-01` | Expected blob `<sha>` missing on the node | re-push from a clone that has it / `tailvault verify` | 5 |

In addition, a config/precondition family (referred to as `TV-CFG`) covers bad
`tailvault.toml`, an unresolvable or missing `location`, and an unparseable
pointer or lock — these map to **exit bucket 2**.

### Exit-code buckets (normative)

| Exit | Meaning |
|---|---|
| `0` | success |
| `2` | config / precondition (bad `tailvault.toml`, no/unknown location, bad pointer or lock) |
| `3` | network / Tailscale down (`TV-NET-*`) |
| `4` | node unreachable (`TV-NODE-*`) |
| `5` | integrity / missing blob (`TV-OBJ-*`) |

The `pre-push` hook surfaces the **same** code so a failed push reads obviously
rather than as a generic git error. (Exit `1` remains the catch-all for an
otherwise-unclassified error; Task 07 owns the code→exit map.)

---

## 6. Resolved Open Questions

| Q | Decision |
|---|---|
| Q1 — Language/runtime | **Go** — single static binary, first-class Tailscale ecosystem, easy cross-compile to the Pi |
| Q2 — First backend | **SSH first**; Taildrive in Block 2 |
| Q3 — Lock conflict policy | **Per-path union merge driver**; assume a single active writer early |
| Q4 — GC trigger | **Mark on push, sweep on explicit `gc`** (with `--dry-run`) — avoids surprise deletes mid-push |
| Q5 — Default `min_size` | **5 MB**, per-project overridable (see [§7](#7-size-unit-binding-minsize) for the unit binding) |
| Q6 — Checkout resolution | **Eager smudge** for v1; lazy/partial as a later option |
| Q7 — Identity stamp | **`tailscale whois`**, fall back to git `user.email` |
| Q8 — Scope of v1 | **MVP first** (SSH; `init/track/status/push/pull/gc`; no history, no Taildrive), then iterate |
| Q9 — Node discovery | **Local-session only** (`tailscale status --json`); no API login, no stored credentials |
| Q10 — Error model | **Structured** typed conditions + stable codes + bucketed exit codes (see [§5](#5-error-catalogue)) |

> Q1–Q8 are the proposal's explicit Open Questions. Q9 (local-session discovery,
> no API login) and Q10 (structured error model) are promoted here from the
> proposal's Node-discovery and Error-model sections so all ten resolved
> decisions live in one frozen table. These match `CLAUDE.md`'s "Locked
> decisions".

---

## 7. Size-unit binding (`min_size`)

**Frozen binding for v1 (decided + implemented by Task 03, `internal/config`):**
tailvault interprets **all** size suffixes as **binary** units (powers of 1024).
This is a single rule for every suffix and matches the proposal's intuition that
"5 MB" is the on-disk threshold.

- `B` (or no suffix) = bytes.
- `KB = 1024`, `MB = 1024²`, `GB = 1024³`, `TB = 1024⁴`.
- IEC spellings are accepted as explicit synonyms for the same values:
  `KiB = KB`, `MiB = MB`, `GiB = GB`, `TiB = TB`.
- Matching is case-insensitive; an optional single space between number and
  suffix is allowed; a fractional number is permitted and truncated to whole
  bytes (e.g. `"1.5MB" → 1 572 864`).
- Therefore the default `"5MB"` parses to **5 242 880 bytes** (= 5 × 1024²).

Canonical test vectors (in `internal/config/size_test.go`):
`"5MB" → 5242880`, `"5MiB" → 5242880`, `"512KB" → 524288`, `"1GiB" → 1073741824`,
`"1048576" → 1048576`, `"1.5MB" → 1572864`; empty / garbage / missing-number /
negative all error.

---

## 8. Frozen Go API names (cross-workstream contract)

To stop workstreams guessing at package symbols (the task files were inconsistent
— e.g. `lock.File` vs `lock.Lock`), these names are **frozen**. Consume them
directly; do not introduce aliases.

| Package | Frozen symbols |
|---|---|
| `internal/lock` | type `Lock` (fields `Version`, `GeneratedBy`, `Entries []Entry`); type `Entry` (`Path`, `SHA256`, `Size`, `Location`, `PushedAt time.Time`, `Pusher`, `History`, `Preserve`, `Deleted bool` (tombstone), `Versions []string`); `Load(path)`, `Parse([]byte)(*Lock,error)`, `Write(path,*Lock,generatedBy)`, `(*Lock).Canonicalize()`, `(*Lock).Upsert(Entry)`, `(*Lock).Remove(path)`, `(*Lock).Find(path)(Entry,bool)`, `(*Lock).ReferencedSHAs()[]string` (current sha + all versions, deduped) |
| `internal/config` | type `Config`/`Storage`/`Rules`/`Override`; `Load(path)`, `(*Config).Validate()`, `Write(path,*Config)`, `Default() Config`, `ParseSize(string)(int64,error)`, `ValidateGlob(string)error`, `(*Config).AddInclude(string)bool` |
| `internal/pointer` | const `Magic`; type `Pointer{SHA256,Size int64,Location}`; `Encode(Pointer)[]byte`, `Decode([]byte)(Pointer,error)`, `IsPointer([]byte)bool` |
| `internal/tserr` | type `Error{Code,Cause,Fix string,Err error}` with `Error()`/`Unwrap()`/`ExitCode()`; `Code` consts `ConfigBad`=TV-CFG-01, `NetNotRunning`=TV-NET-01, `NetNotLoggedIn`=TV-NET-02, `NodeOffline`=TV-NODE-01, `NodeNotWritable`=TV-NODE-02, `ObjMissing`=TV-OBJ-01; constructors `ConfigErr(cause,err)`, `NetNotRunningErr(err)`, `NetNotLoggedInErr(err)`, `NodeOfflineErr(node,err)`, `NodeNotWritableErr(node,err)`, `ObjMissingErr(sha,err)`; `ExitCodeFor(error)int` |

**Error-layering rule (per task-06):** leaf data packages (`config`, `lock`,
`pointer`) return plain `error` values; commands wrap them in a typed
`tserr.Error` at the boundary so the exit-code bucket is correct (config/parse
failures → `tserr.ConfigErr` → exit 2). All `tserr` constructors take a trailing
`err error` — pass `nil` when there is no underlying error.

---

# Part 2 — Federation contract (v2)

> **Normative, additive.** Sections §9–§16 freeze the federation/vault formats the
> same way §1–§8 froze v1. v1 sections are **untouched** (D29: no real v1 vaults
> exist, so no migration machinery). Every Block 3–4 task cites a section here
> instead of re-deciding a format. Frozen decisions trace to
> [`BRAINSTORM-block-3.md`](./BRAINSTORM-block-3.md) (D1–D31) as distilled into
> [`proposal.md`](./proposal.md) Part II — do not re-litigate them.
>
> **All v1 conventions carry forward unchanged:** TOML; RFC3339 UTC timestamps
> with a `Z` suffix; binary size units (§7); byte-wise-ascending canonical
> ordering. Where v2 froze a remaining mechanical detail (file layout on the node,
> field names, PHC string form) it picked the choice consistent with these
> conventions; such picks are called out inline.

## 9. Catalog schema (`meta/catalog.toml`)

The catalog makes every storage location **self-describing**. It lives at
`<base_path>/<subpath>/meta/catalog.toml` (the same `<base_path>/<subpath>` root
as v1 `objects/`). It is written with the v1 atomicity discipline: temp file +
`fsync` + atomic rename.

### Storage layout on the node (v2 — extends §4)

```
<base_path>/<subpath>/
  objects/<sha256>              # v1: content-addressed blobs, deduped
  refs/<path-id>               # v1: history-on files only, newest-first shas
  meta/catalog.toml            # §9  self-describing vault state
  meta/wal/<seq>-<op_id>.toml  # §10 hash-chained per-node WAL (+ sibling markers)
  meta/auth/passwd             # §16 argon2id password hash, mode 0600
  meta/manifest.json           # v1: optional bookkeeping (unchanged)
```

### Top-level fields

| Field | Type | Notes |
|---|---|---|
| `version` | int | MUST equal `2`. A reader hitting an **unknown** version MUST fail with a config-style incompatibility error → **exit 2** (H7), exactly like a bad `tailvault.toml`. |
| `vault_name` | string | human label for this location's vault |
| `node` | string | the MagicDNS name / `100.x` IP this catalog describes |
| `[federation]` | table | roster — see [§13](#13-federation-roster-section) |
| `[[file]]` | array of tables | one per tracked file — see below |

### Per-file `[[file]]` fields (canonical order)

Field order **within** each entry is fixed and is the order below (mirrors the v1
lock convention). `[[file]]` entries are sorted by `path`, **byte-wise
ascending**, stable across writes (mirror lock canonical form, §2).

| Field | Type | Notes |
|---|---|---|
| `id` | string | 64-hex genesis hash (§11). The stable, location-independent file ID. |
| `genesis` | inline table | the birth record: `content_sha256`, `original_path`, `ingest_op_id`, `origin_node` (§11). Embedded so the catalog is itself an identity backup. |
| `sha256` | string | **current** content hash. For `manual` files this drifts from `genesis.content_sha256` after in-place edits until a `scan` re-hashes (H12). |
| `path` | string | vault-relative **logical** path (display/navigation; moves change it, never `id`). |
| `sync_mode` | string | `"git"` \| `"manual"`. **Enum is extensible** (D15): an unknown value MUST be preserved on round-trip and treated as **not-git** by gc (i.e. never a gc candidate). |
| `size` | int | bytes (binary units per §7 apply only to human-typed sizes; this is a raw count). |
| `created_at` | string | RFC3339 UTC `Z` — first ingest into the catalog. |
| `updated_at` | string | RFC3339 UTC `Z` — last catalog mutation for this file. |
| `last_scanned` | string | RFC3339 UTC `Z` — last `vault scan` that re-hashed/verified the on-disk bytes (drives edited-vs-corrupt logic, H12). |

### Sample (canonical form — the byte-exact `catalog.Encode` output, used as the fixture)

> **Canonical rendering.** This is the **exact** output of `catalog.Encode`
> (`pelletier/go-toml/v2` v2.2.2, the repo's TOML library, mirroring how
> `internal/lock` renders §2). The canonical form therefore uses go-toml/v2's
> native rendering: **single-quoted literal strings** and **bare (unquoted)
> RFC3339 datetimes**. Task 28 pastes this block into `internal/catalog/testdata/`
> and asserts `Parse → Encode` is byte-identical. (The other v2 samples below are
> illustrative valid TOML; each implementing task freezes its own canonical bytes
> via its encoder + testdata the same way.)

```toml
version = 2
vault_name = 'root-pnp'
node = 'home-pi.tailnet-name.ts.net'

[federation]
fed_id = '5f3c9a1e7b8d2c40a16e9f0b3d4c5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d'

[[federation.member]]
name = 'home-pi'
node = 'home-pi.tailnet-name.ts.net'
joined_at = 2026-06-11T09:00:00Z
status = 'active'

[[federation.member]]
name = 'office-nas'
node = '100.92.14.7'
joined_at = 2026-06-11T09:05:00Z
status = 'active'

[[file]]
id = '30092d830e2641b447745655bbe4171675720a1aa8cf80e0ae3736e6e43111f0'
genesis = {content_sha256 = 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855', original_path = 'pnp/board.pdf', ingest_op_id = '0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6', origin_node = 'home-pi'}
sha256 = 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'
path = 'pnp/board.pdf'
sync_mode = 'manual'
size = 41231873
created_at = 2026-06-11T09:10:00Z
updated_at = 2026-06-11T09:10:00Z
last_scanned = 2026-06-11T09:10:00Z
```

### 9b. `.tailvaultignore` semantics

A repo-root / **vault-root** file named `.tailvaultignore`, gitignore-style
**doublestar** glob patterns (same engine as `tailvault.toml` `include`/`exclude`,
§1). It is **opt-out only** (it removes paths from bootstrap track-all, D18 path
2); it is **overridden by an explicit `track`** (D22) — i.e. a path the user
explicitly tracks is ingested even if `.tailvaultignore` would skip it. Absence
of the file means "track everything" on bootstrap.

## 10. WAL entry schema + hash-chain rule (`meta/wal/`)

Every storage node keeps its own hash-chained write-ahead log under
`<base_path>/<subpath>/meta/wal/`. The federated layer is **stateless** — a node's
WAL is the only record of its own ops (D6). One TOML file per entry, named
`<seq>.toml` where `<seq>` is **zero-padded to 12 digits** (`000000000001.toml`).
Appending an entry is an **atomic `Put` of the next-seq key** (temp + fsync +
rename) — never an in-place edit of an existing entry.

> **Slot filename freeze (DG-29.1).** The op id lives **inside** the entry, NOT
> in the filename, so that two clients racing the same seq write the **same key**
> — backend `Put` dedup then makes the first write stick (the slot file *is* the
> per-blob lock claim). A loser reads the slot back, sees a different `op_id`, and
> retries at the next seq; a same-blob loser is caught by the pending-intent check
> and gets `TV-FED`/`op-in-flight`. (Task-27's brief sketched `<seq>-<op_id>.toml`,
> but that name defeats the very dedup its own race-resolution rule relies on, so
> v2 freezes `<seq>.toml`. Markers are keyed by seq for the same reason.) True
> multi-writer safety additionally requires the backend's `Put` to be
> create-exclusive; early operation assumes a single active writer (Q3/D12).

### Entry fields

| Field | Type | Notes |
|---|---|---|
| `seq` | int | monotonic per node, starting at `0` for the genesis entry. |
| `op_id` | string | UUID, lowercase hex, no dashes (minted by `wal.NewOpID`; bootstrap derives a deterministic one — DG-29). Unique → idempotent retry/dedupe. Sample values are illustrative. |
| `prev_hash` | string | 64-hex sha256 over the **canonical on-disk bytes of the previous entry**. The genesis entry (`seq = 0`) uses **64 zeros**. |
| `op_type` | string | one of `ingest` \| `move` \| `delete` \| `sync_mode` \| `gc` \| `roster` \| `scan` \| `passwd`. (`passwd` = password-rotation op, task-46/DEV-46.6; `blob_refs = ["meta/auth/passwd"]` so concurrent rotations serialize via WAL-as-lock.) |
| `blob_refs` | []string | file **IDs** (§11) this op locks — the basis of WAL-as-lock (D12). |
| `actor` | string | identity from `tailscale whois`, falling back to git `user.email` (§6 Q7). |
| `created_at` | string | RFC3339 UTC `Z`. |
| `args` | table | op-typed argument table (shape defined by the op; e.g. `move` carries `from`/`to`/`moved_to`). Emitted last (TOML tables follow scalars). |

> **No `state`/`updated_at` in the entry (DG-27.2).** The persisted entry is
> immutable so the hash chain never re-hashes; it therefore carries no mutable
> state. Effective state lives entirely in sibling markers (below).

### Hash-chain rule (normative)

Each entry's hash is `sha256` over its **entire canonical serialized bytes**
(excluding nothing). `prev_hash` links entry *n* to entry *n−1*. Any reader
replaying the chain MUST verify **every** link (and seq contiguity) and **fail**
on the first break (tamper-evident, D17) → **`TV-FED-03`**, exit bucket 6.
Verification hashes the **raw on-disk bytes** of each entry file (not a
re-encode), so any byte-level tampering is detected and the chain survives an
encoder change — do NOT re-encode-on-verify.

The canonical byte form is `wal.Encode`, produced by **explicit byte
construction** — NOT a TOML marshaler. This is load-bearing: these bytes feed the
hash chain (and `fed_id`), so the serialization must be byte-stable forever and
independent of any library's rendering (a go-toml bump must never silently change
an on-disk chain → spurious `TV-FED-03`). The output is still valid TOML (Decode
reads it back). Frozen format: fixed field order, LF endings, **double-quoted
basic strings** with TOML escaping, `blob_refs` as an inline array, `created_at`
as a **bare RFC3339Nano UTC** datetime, the `args` table **last with sorted keys**
(omitted entirely when empty). **Frozen hash test vector** (Task 29 reproduces it
byte-for-byte): the genesis sample below hashes to
`bb55bed553d0ba5a797d2dbca8a041a073b7481fea5cf5fcb4f735979793cbc3`.

### Immutability + state markers (normative — do NOT simplify to in-place edits)

The entry file is **immutable** once written, so the chain never re-hashes on a
state change. A terminal transition is recorded by writing a **sibling marker
file** keyed by seq:

- `<seq>.done` — op completed.
- `<seq>.failed` — op failed (surfaces in `ops`, retryable).

Each marker is a small TOML: `{ seq, op_id, state ("done"|"failed"), at (RFC3339
UTC Z), reason? }`. The **effective state** of an op = the marker's state if a
marker exists (done wins over failed), else `intent`. Write-ahead ordering
(proposal Part II atomicity standards): WAL intent → blob bytes → catalog → WAL
`.done` marker; a crash anywhere is detectable and repairable by `verify`/`heal`.

Done entries are pruned by journal gc (H9): `Prune` removes the **leading run** of
done entries older than a `keep` window and records the last pruned entry's
`{seq, hash}` as a **forward-only anchor marker** `meta/wal/pruned/<seq>`, so the
surviving chain still anchors and verifies. The effective anchor is the
**highest-seq** marker. Advancing the anchor is a single `Put` of a NEW key
(the live anchor is never deleted before its successor exists), so a crash during
`Prune` can never leave the chain anchorless. `Prune` never prunes intent/failed
entries (it stops at the first non-done-or-recent entry), and a reader ignores any
entry at or below the effective anchor seq (crash-safe: the anchor is written
before the old files are deleted).

### Sample genesis entry (canonical `wal.Encode` form — used as the test vector)

```toml
seq = 0
op_id = "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6"
prev_hash = "0000000000000000000000000000000000000000000000000000000000000000"
op_type = "ingest"
blob_refs = ["30092d830e2641b447745655bbe4171675720a1aa8cf80e0ae3736e6e43111f0"]
actor = "ibte@laptop"
created_at = 2026-06-11T09:10:00Z

[args]
content_sha256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
origin_node = "home-pi"
path = "pnp/board.pdf"
sync_mode = "manual"
```

## 11. Genesis record + file-ID derivation

A file's **ID** is the sha256 of its **genesis record** — its birth fact, taken
from the ingest WAL entry. Dual addressing (D19): the ID is stable and
location-independent (used for all linking/lock/reference); the logical `path`
(§9) is for display/navigation. **Moves change the path, never the ID.**

### Genesis record

The record has exactly four fields in this **fixed order**:
`content_sha256`, `original_path`, `ingest_op_id`, `origin_node`.

### Canonical serialization (byte-exact — load-bearing)

> A whitespace difference changes every file ID in existence. The serialization is
> frozen as **explicit byte construction**, NOT "run a TOML encoder" (encoder
> output varies across libraries).

Produce exactly four lines, in the field order above, each of the form:

```
key = "value"
```

- exactly one ASCII space on each side of `=`;
- the value is enclosed in double quotes and escaped with **TOML basic-string
  escaping** (`\` → `\\`, `"` → `\"`, and the control escapes `\b \t \n \f \r`,
  other control chars as `\uXXXX`);
- each line terminated by a **single LF** (`\n`), **including the last line**;
- UTF-8, no BOM, no blank lines, no leading/trailing whitespace.

Then `id = lowercasehex( sha256( those bytes ) )`.

### Properties

- **Unique** — `ingest_op_id` + `original_path` salt the hash, so two identical
  files ingested at different paths/times get different IDs.
- **Location-independent** — nothing about the current home is in the record;
  moves never touch it.
- **Regeneratable** — anyone holding the genesis record recomputes the ID
  byte-for-byte (no random state, no counter). NOT regeneratable from the file
  bytes alone (manual files drift — H12), which is why the record is the input.
- **Self-certifying** — `sha256(record) == id` proves the record (basis of
  identity recovery, D24).
- **Short display form** — the first **12 hex** chars (like a git short SHA).
- The ID is **NOT** the content hash (D19/H12).

### Worked example (test vector — Task 30 MUST reproduce byte-for-byte)

Record fields:
`content_sha256 = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`
(the sha256 of empty input, a recognizable constant),
`original_path = pnp/board.pdf`, `ingest_op_id = 0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6`,
`origin_node = home-pi`.

Canonical bytes (each line LF-terminated):

```
content_sha256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
original_path = "pnp/board.pdf"
ingest_op_id = "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6"
origin_node = "home-pi"
```

⇒ `id  = 30092d830e2641b447745655bbe4171675720a1aa8cf80e0ae3736e6e43111f0`
⇒ short = `30092d830e26`

### Identity recovery (cross-reference)

Because the ID is self-certifying, the genesis record is replicated for recovery
(D24): **lock entries embed the full genesis record** (D24a — every repo clone is
an off-node identity backup; the lock-schema-v2 change that carries it is
specified in **Task 35**, not here), every `vault get` writes a **pull receipt**
(§12), and `vault restore-identity` re-seeds a rebuilt catalog after verifying
`sha256(record) == id` (never implicit).

## 12. Pull receipt format (`~/.tailvault/receipts/<id>.toml`)

Written by **every `vault get`** (D24b); read by `vault restore-identity` (Block
4). Client-side, one file per file-ID.

| Field | Type | Notes |
|---|---|---|
| `id` | string | 64-hex file ID. |
| `genesis` | inline table | the full genesis record (§11) — the recoverable birth fact. |
| `path` | string | logical path at pull time. |
| `sha256_at_pull` | string | content hash of the bytes actually downloaded. |
| `pulled_at` | string | RFC3339 UTC `Z`. |
| `source_node` | string | the member the bytes came from. |

```toml
id = "30092d830e2641b447745655bbe4171675720a1aa8cf80e0ae3736e6e43111f0"
genesis = { content_sha256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", original_path = "pnp/board.pdf", ingest_op_id = "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6", origin_node = "home-pi" }
path           = "pnp/board.pdf"
sha256_at_pull = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
pulled_at      = "2026-06-11T10:00:00Z"
source_node    = "home-pi.tailnet-name.ts.net"
```

## 13. `[federation]` roster section

Lives inside each member's catalog (§9). The roster is mirrored across members;
each member's catalog carries the same logical roster (propagated by WAL `roster`
ops, §10).

| Field | Type | Notes |
|---|---|---|
| `fed_id` | string | 64-hex; minted at `fed init` as the sha256 of the **whole seq-0 (genesis) WAL entry's canonical bytes** per §10 (`wal.Hash` of the init entry) — NOT the §11 file-ID "genesis record" (which is the 4-field birth record). The two "genesis" hashes differ in input: fed_id hashes the entire WAL entry; a file ID hashes the 4-field record. Stable federation identity. |
| `[[federation.member]].name` | string | member label. |
| `[[federation.member]].node` | string | MagicDNS / `100.x`. |
| `[[federation.member]].joined_at` | string | RFC3339 UTC `Z`. |
| `[[federation.member]].status` | string | `active` \| `left` \| `evicted`. |

**Lifecycle:** `leave` and `evict` **keep the member row** with a status change
(never delete it) — history matters for the WARN messages readers see (D28). A
`left`/`evicted` member's files drop out of the federated tree; its disk is
untouched (D28).

## 14. Client cache format (`~/.tailvault/cache/fed-<fed_id>/`)

Two files: `current.toml` and `previous.toml`. On **every successful federation
read** the client rotates `current → previous` and writes a fresh `current`.
**Advisory only — live pings always win** (D26). Used to distinguish "was here,
now offline" from "never existed" (improves partial-view UX, H4) and to detect
roster/state changes.

| Field | Type | Notes |
|---|---|---|
| `fed_id` | string | 64-hex (matches §13). |
| `taken_at` | string | RFC3339 UTC `Z` snapshot time. |
| `[[member]].name` / `.node` / `.status` | string | roster snapshot. |
| `[[member]].reachable` | bool | reachability at snapshot time. |
| `[[member]].last_seen` | string | RFC3339 UTC `Z`, last time this member answered. |
| `[[member]].file_count` | int | per-member catalog summary. |
| `[[member]].ids` | []string | file IDs the member reported (summary). |

```toml
fed_id   = "5f3c9a1e7b8d2c40a16e9f0b3d4c5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d"
taken_at = "2026-06-11T10:00:00Z"

[[member]]
name       = "home-pi"
node       = "home-pi.tailnet-name.ts.net"
status     = "active"
reachable  = true
last_seen  = "2026-06-11T10:00:00Z"
file_count = 1
ids        = ["30092d830e2641b447745655bbe4171675720a1aa8cf80e0ae3736e6e43111f0"]
```

## 15. `TV-FED-*` codes + exit bucket 6 (extends §5)

| Code | Cause | Fix | Exit bucket |
|---|---|---|---|
| `TV-FED-01` | partial view — object not found among **reachable** members and **≥1 member unreachable** ("cannot prove absence") | bring the offline member(s) online and retry, or `tailvault ops`/cache to see last-known state | 6 |
| `TV-FED-02` | a federation op that needs **ALL** members (gc) ran with **≥1 member unreachable** (D27/R3 gate) | bring all members online and retry; deletes never tolerate partial views | 6 |
| `TV-FED-03` | WAL **hash-chain verification failed** (tamper / corruption) | inspect with chain-verify tooling (Block 5); restore the affected node's WAL from a clone/backup | 6 |
| `TV-AUTH-01` | password missing/rejected on a mutating remote op (mv, rm, sync-mode change, remote gc, evict, roster writes incl. `fed join`/`leave`/`evict`) | re-run with the correct password, or reset the hash over SSH/physical access | 2 |

### Exit-code buckets (extends §5)

| Exit | Meaning |
|---|---|
| `6` | federation / partial view (`TV-FED-*`) — a reachability-scoped read or all-members op could not be satisfied because ≥1 member was unreachable, or a WAL chain failed verification |

(`TV-AUTH-01` reuses bucket **2** — precondition/auth; the op is refused before
any work, exactly like a config precondition. No new bucket.)

### Resolution semantics (normative — restated)

For a `get`/`stat`/`mv`/`rm` targeting a specific file:

1. **Found at its recorded home** → success.
2. **Found at a different reachable member** (via fan-out or a `moved_to`
   forwarding record) → success **+ WARN** ("home moved — run `tailvault heal`").
3. **Not found among reachable members AND ≥1 member unreachable** →
   **`TV-FED-01`** (exit 6) — cannot prove absence.
4. **Not found, all members reachable, no pending `move`** → **`TV-OBJ-01`**
   (exit 5) — genuinely missing.

This `TV-FED-01` (exit 6) vs `TV-OBJ-01` (exit 5) distinction is a hard
invariant. Every remote view carries reachability metadata.

## 16. Password hash file + auth (`meta/auth/passwd`)

A single-line **argon2id** hash in canonical **PHC string format**, at
`<base_path>/<subpath>/meta/auth/passwd`, mode **`0600`**:

```
$argon2id$v=19$m=65536,t=3,p=4$<salt-b64>$<hash-b64>
```

- Produced via `golang.org/x/crypto/argon2` (`IDKey`) — **never roll our own
  crypto** (D8).
- Parameters frozen: `m = 65536` (64 MiB), `t = 3`, `p = 4`; **16-byte** random
  salt; **32-byte** derived key.
- `<salt-b64>` / `<hash-b64>` are standard-alphabet base64 **without padding**
  (`=` stripped), per the PHC string spec.
- **No recovery:** reset requires SSH/physical access to rewrite the file (D9, H8).

> **Mechanical detail frozen (DG-27.1):** task-27's brief wrote the string without
> the leading `$`; SPEC v2 freezes the **canonical PHC form with the leading `$`**
> (and unpadded base64), which is what `x/crypto` consumers interoperate with.

### Reads are never password-gated (normative)

The password authorizes **mutating remote ops only**. **Reads
(`ls`/`stat`/`get`/`search`) are NEVER password-gated** — they ride the tailnet
ACL + SSH alone (D9). State this rule wherever a command checks auth.

### `TV-AUTH-01` (see §15 table)

Missing/rejected password on a mutating remote op → `TV-AUTH-01`, **exit bucket
2**. The op is refused before any work.

### Roster writes are gated (explicit ruling)

`fed join` — and **every** other roster update: `leave` applied remotely, and
`evict` — writes the `[federation]` section (§13) of **each member's** catalog and
is therefore a **mutating op on each member: it IS password-gated** per the
default rule. **Each member's own password authorizes the roster write on that
member.** Pending roster ops queued for currently-unreachable members carry the
**same** requirement when later retried. Block 4's `fed` tasks cite this ruling
directly.

## 8b. Frozen Go API names (v2 — extends §8)

Reserved so workstreams don't guess at symbols; Tasks 28–31 fill in the detail.
Consume these directly; do not introduce aliases.

| Package | Reserved symbols |
|---|---|
| `internal/catalog` | type `Catalog` (fields incl. `Version`, `VaultName`, `Node`, `Federation`, `Files []File`); type `File` (`ID`, `Genesis`, `SHA256`, `Path`, `SyncMode`, `Size`, `CreatedAt`/`UpdatedAt`/`LastScanned time.Time`) |
| `internal/wal` | type `Entry` (`Seq`, `OpID`, `PrevHash`, `OpType`, `BlobRefs`, `Actor`, `CreatedAt`, `Args`) — **no `State`/`UpdatedAt`** (immutable entry; DG-27.2); effective state is on `type Rec` (`Entry`, `State`) returned by `Read`/`Pending`; type `Log` (append/read/verify-chain/prune) |
| `internal/identity` | type `Genesis` (`ContentSHA256`, `OriginalPath`, `IngestOpID`, `OriginNode`); `MintID(Genesis) string`; type `Receipt` |
| `internal/fed` | type `Roster` (`FedID`, `Members []Member`); type `Member` (`Name`, `Node`, `JoinedAt`, `Status`); type `Snapshot` (client cache) |

## Cross-references

- [`proposal.md`](./proposal.md) — Detailed Design (all schema blocks), Error
  model, Node discovery, Open Questions.
- [`DESIGN.md`](./DESIGN.md) — §4 versioning/retention model, §5 Tailscale
  leverage, §6 surfaces, the golden schema dump.
- [`tasks/`](./tasks/) — per-task acceptance criteria that cite this spec.
