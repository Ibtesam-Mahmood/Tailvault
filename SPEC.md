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
  `preserve`, then `versions` (history-on entries only).
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

## Cross-references

- [`proposal.md`](./proposal.md) — Detailed Design (all schema blocks), Error
  model, Node discovery, Open Questions.
- [`DESIGN.md`](./DESIGN.md) — §4 versioning/retention model, §5 Tailscale
  leverage, §6 surfaces, the golden schema dump.
- [`tasks/`](./tasks/) — per-task acceptance criteria that cite this spec.
