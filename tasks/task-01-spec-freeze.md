# Task 01: Spec Freeze — schemas + error catalogue + resolved Open Questions

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Detailed Design (all schema blocks), Error model, Node discovery, Open Questions · **Block:** 1 (MVP) · **Estimated Effort:** 0.5 day · **Dependencies:** None — can start anytime · **Type:** Foundation

## Summary

Before a single line of Go is written, we freeze the four file schemas and the error-code catalogue so the rest of the MVP can be built against a stable contract. The proposal's "Phase 0 — Decisions & spec freeze" exists precisely to stop downstream churn: if `tailvault.toml`, `tailvault.lock`, the pointer format, or `locations.toml` keep mutating while `internal/config`, `internal/lock`, and `internal/rules` are being implemented, every parser test and round-trip guarantee becomes a moving target.

The deliverable is a new `SPEC.md` at the repo root. It is the single normative reference the implementation tasks (02–19) cite when they say "mirror the proposal's config block" or "use the canonical lock ordering". It restates — precisely and with defaults, validation rules, and canonical orderings — every field of the four schemas, the full error catalogue (codes, causes, fixes, exit-code buckets), and the resolved answers to Open Questions Q1–Q10.

End-state: `SPEC.md` exists, is internally consistent with `proposal.md` and `DESIGN.md`, and is referenced by `CLAUDE.md`'s planned-structure section. No Go code is produced. The review checklist below (not unit tests) gates completion.

## Context

### Related packages
- None yet (no Go module exists). This task feeds `internal/config` (Task 03), `internal/lock` (Task 04), `internal/rules` (Task 05), `internal/pointer` (Task 06), `internal/tserr` (Task 07), and `internal/locations` (Task 10).

### Prerequisites
- [ ] `proposal.md` read in full (Detailed Design, Error model, Node discovery, Open Questions).
- [ ] `DESIGN.md` available for cross-reference (schemas, retention model).
- [ ] `CONTRIBUTING.md` versioning rule understood (VERSION + CHANGELOG bump per task).

## Changes Required

**File:** `SPEC.md` (repo root)
**Action:** Create.
**Purpose:** Frozen normative spec for the four schemas, the error catalogue, and the resolved Open Questions. No Go code.

Document the following, verbatim-precise:

### 1. `tailvault.toml` (repo-committed project config)

| Field | Type | Default | Validation |
|---|---|---|---|
| `version` | int | `1` | MUST equal `1`; else config error (TV-CFG / exit 2) |
| `[storage].location` | string | — (required) | non-empty; resolved against `locations.toml` |
| `[storage].subpath` | string | `""` | optional child folder under the location `base_path` |
| `[rules].min_size` | string | `"5MB"` | human size; parses to bytes (see Q5) |
| `[rules].include` | []string | `[]` | doublestar globs (`**/*.pdf`, …) |
| `[rules].exclude` | []string | `[]` | doublestar globs; exclude wins over include |
| `[rules].history` | bool | `false` | global default; anti-bloat |
| `[rules].auto_delete` | bool | `true` | prune blob on git delete |
| `[[rules.overrides]].match` | string | — | doublestar glob; **first match wins** |
| `[[rules.overrides]].history` | bool | inherits | per-pattern override |
| `[[rules.overrides]].preserve` | bool | `false` | never auto-delete when true |

Include the proposal's exact sample block (`location = "home-pi"`, `subpath = "root-pnp"`, `min_size = "5MB"`, include/exclude lists, the `masters/**` override with `history = true`, `preserve = true`).

### 2. `tailvault.lock` (repo-committed state, canonical form)

Entry fields and **canonical ordering**:
- Entries are `[[entry]]` tables sorted by `path` (byte-wise ascending), stable across writes.
- Field order within each entry is fixed: `path`, `sha256`, `size`, `location`, `pushed_at`, `pusher`, `history`, `preserve`, then `versions` (history-on only).
- `versions = ["<newest>", …, "<oldest>"]` — **newest-first**.
- `pushed_at` is RFC3339 UTC (`2026-06-10T18:22:04Z`).
- `pusher` from `tailscale whois`, falling back to git `user.email` (Q7).
- Top-level: `version = 1`, `generated_by = "tailvault <ver>"`.

This canonical form is what makes the per-path union merge driver (Task 24) produce minimal, conflict-free diffs.

### 3. Pointer file (in-git stand-in)

Exact four-line format, in order:
```
tailvault.v1
sha256 <hex>
size <bytes>
location <name>
```
First line is the literal magic `tailvault.v1`. Fields are `key SP value`. No trailing keys; parser rejects unknown magic.

### 4. `locations.toml` (user-level, `~/.config/tailvault/`, NOT in repo)

| Field | Filled by | Notes |
|---|---|---|
| `node` | discovery or `--node` | MagicDNS name or `100.x` IP |
| `base_path` | prompt | e.g. `/mnt/ssd/tailvault` (USB3 SSD, not boot SD) |
| `backend` | prompt | `ssh` \| `taildrive` |
| `user` | prompt (ssh) | SSH user |
| `share` | prompt (taildrive) | Taildrive share name |

`node` is prefilled from `tailscale status --json` peer enumeration (local session only — no API/login). `base_path`, `backend`, `user`/`share` come from interactive prompts.

### 5. Error catalogue

| Code | Cause | Fix | Exit bucket |
|---|---|---|---|
| `TV-NET-01` | `tailscaled` not reachable / `tailscale` not in PATH | start Tailscale; run `tailscale status` | 3 |
| `TV-NET-02` | Not logged into the tailnet | `tailscale up` | 3 |
| `TV-NODE-01` | Storage node offline/unreachable (not in `status`, or `ping`/`Stat` failed) | power on/connect node; `tailvault location ls` | 4 |
| `TV-NODE-02` | Node reachable but `base_path` not writable | check SSH user / Taildrive share + perms | 4 |
| `TV-OBJ-01` | Expected blob `<sha>` missing on node | re-push from a clone that has it / `verify` | 5 |

Exit-code buckets: `0` success; `2` config/precondition (bad `tailvault.toml`, no location); `3` network/Tailscale down; `4` node unreachable; `5` integrity/missing blob. The `pre-push` hook surfaces the same code.

### 6. Resolved Open Questions

| Q | Decision |
|---|---|
| Q1 Language | **Go** (single static binary, Tailscale ecosystem, cross-compile to Pi) |
| Q2 First backend | **SSH first**; Taildrive in Block 2 |
| Q3 Lock conflicts | **Per-path union merge driver**; assume single active writer early |
| Q4 GC trigger | **Mark on push, sweep on explicit `gc`** (with `--dry-run`) |
| Q5 `min_size` | **5 MB** (document MB vs MiB binding — see Task 03), per-project overridable |
| Q6 Checkout resolution | **Eager smudge** for v1; lazy later |
| Q7 Identity stamp | **`tailscale whois`**, fall back to git `user.email` |
| Q8 Scope | **MVP first** (SSH; `init/track/status/push/pull/gc`; no history, no Taildrive), then iterate |
| Q9 Node discovery | **Local-session only** (`tailscale status --json`); no API login, no stored credentials |
| Q10 Error model | **Structured** typed conditions + stable codes + bucketed exit codes |

> Note: the proposal lists Q1–Q8 explicitly; Q9 (local-session discovery, no API login) and Q10 (structured error model) are promoted here from the proposal's Node-discovery and Error-model sections so all ten resolved decisions live in one frozen table.

**Implementation Notes:** Pure documentation. Quote the proposal's sample blocks verbatim so implementers can paste them into fixtures. Cross-link `DESIGN.md` where it carries the golden schema dump.

**Key Considerations:** This file is normative — when proposal and SPEC disagree later, a follow-up issue reconciles them; do not silently diverge. State the MB-vs-MiB binding as a TODO that Task 03 must resolve and back-fill here.

## Implementation Checklist
- [ ] Create `SPEC.md` at repo root.
- [ ] Document `tailvault.toml` fields/defaults/validation + sample.
- [ ] Document `tailvault.lock` entry fields + canonical ordering + sample.
- [ ] Document pointer 4-line format.
- [ ] Document `locations.toml` fields + which are discovery-filled.
- [ ] Document the error catalogue (codes → cause → fix → exit bucket).
- [ ] Record resolved Q1–Q10.
- [ ] Reference `SPEC.md` from `CLAUDE.md` planned-structure section.

## Testing Requirements

N/A — no code. Gated by a **review checklist** instead:
- [ ] Every `tailvault.toml` field in the proposal appears with a default + validation rule.
- [ ] Lock canonical ordering (sort-by-path, fixed field order, versions newest-first) is unambiguous.
- [ ] Pointer format matches the proposal's four lines exactly.
- [ ] All five error codes documented with the correct exit bucket (0/2/3/4/5).
- [ ] All ten Open Questions have a recorded decision matching `CLAUDE.md`'s "Locked decisions".
- [ ] No contradiction with `proposal.md` / `DESIGN.md`.

## Validation Checklist
- [ ] `SPEC.md` renders cleanly (tables, code fences).
- [ ] N/A: `go build ./...`, `go test ./...`, `go vet ./...`, `gofmt -l .` (no Go yet — note as N/A).
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to `CHANGELOG.md` in the same commit (per CONTRIBUTING.md).

## Acceptance Criteria
- `SPEC.md` exists at repo root and covers all four schemas, the error catalogue, and Q1–Q10.
- The review checklist passes with zero open contradictions against the proposal.
- `CLAUDE.md` links to `SPEC.md`.
- `VERSION` and `CHANGELOG.md` bumped in the same commit.

## Related Proposal Sections
- **Detailed Design** — `tailvault.toml`, `tailvault.lock`, pointer, `locations.toml`, storage layout blocks (quoted verbatim).
- **Error model** — "A small set of typed conditions, each with a stable code, a one-line cause, and a concrete next step"; exit codes "0 success; 2 config/precondition; 3 network/Tailscale down; 4 node unreachable; 5 integrity/missing blob."
- **Node discovery** — "reads the local, already authenticated Tailscale session via `tailscale status --json`… No Tailscale login or API token is involved."
- **Open Questions** — Q1–Q8 with their bold recommendations.

## Notes & Considerations
- **Gotcha:** the lock's `versions[]` newest-first ordering is load-bearing for `revert` (Task 21) and GC keep-set construction (Task 16); get the direction right and state it explicitly.
- **Gotcha:** `min_size` MB-vs-MiB is genuinely ambiguous — freeze the binding here once Task 03 decides, so config tests have one true answer.
- **For Next Task:** Task 02 reads `SPEC.md` to know exactly which subcommands the Cobra skeleton must stub (`setup, init, location add/ls, track, status, push, pull, gc, verify, revert`) and that `--version` must come from `VERSION` (never hardcoded).
- Next: [task-02-go-module-cli-skeleton.md](./task-02-go-module-cli-skeleton.md)
