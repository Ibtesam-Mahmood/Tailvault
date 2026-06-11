# Task 27: SPEC v2 Freeze — Federation Schemas, Error Codes & Edge-Case Log

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Core designs" (all subsections), "Edge-case discipline"; Part II task breakdown → 3.1 · **Block:** 3 — Vault catalog + federation core · **Estimated Effort:** 1 ideal eng-day · **Dependencies:** Blocks 1–2 shipped (Task 25 — SPEC.md v1 sections §1–§8 exist and stay untouched) · **Type:** Foundation

## Summary

Part II makes every storage location **self-describing** (a catalog), **tamper-evident** (a hash-chained per-node WAL), and **federated** (a roster + client caches + new error semantics). Before any of that is implemented, the formats must be frozen the same way Task 01 froze the v1 schemas: this task appends a **"SPEC v2 — Federation"** part to `SPEC.md` containing the catalog schema, the WAL entry schema + hash-chain rule, the genesis record + file-ID derivation, the pull receipt format, `.tailvaultignore` semantics, the catalog `[federation]` roster section, the client cache format, the `TV-FED-*` error codes + exit bucket 6, and the per-node password hash file format (argon2id). Every other Block 3–4 task cites these sections instead of re-deciding formats.

This is a docs-only task (no Go code), but it is normative: where a sample block appears here, implementers paste it into test fixtures verbatim, exactly as they do with v1 §1–§4. The frozen decisions come from `BRAINSTORM-block-3.md` (D1–D31) as distilled into proposal Part II — do not re-litigate them; where this task must pick a remaining mechanical detail (file layout on the node, field names), it freezes a choice consistent with the v1 conventions (TOML, canonical ordering, RFC3339-UTC timestamps, binary size units per §7).

The task also creates `EDGE-CASES.md` at the repo root — the running log Block 7's design consumes. Per D31, the discipline starts **now**: every Block 3–6 task's Notes section reminds devs to append discovered edge cases there.

## Context

### Related packages

- No Go packages — `SPEC.md` and `EDGE-CASES.md` only.
- Downstream consumers: `internal/catalog` (Task 28), `internal/wal` (Task 29),
  `internal/identity` (Task 30), `internal/fed` (Tasks 31–32), every Block 3–4
  command task.

### Prerequisites

- [ ] Blocks 1–2 merged; `SPEC.md` §1–§8 stable (v1 sections are **not** edited,
  only appended after them).
- [ ] Read proposal Part II in full + `BRAINSTORM-block-3.md` D1–D31, H1–H12.
- [ ] Confirm with the maintainer if any frozen detail below must deviate from
  Part II (per `CLAUDE.md`, deviations need sign-off).

## Changes Required

### SPEC.md — new "Part 2 — Federation contract (v2)" sections (§9–§16)

- **File:** `SPEC.md`
- **Action:** modify (append new sections; never edit §1–§8)
- **Purpose:** the normative v2 contract.

Sections to write, with required content:

**§9 Catalog schema** (`meta/catalog.toml` under `<base_path>/<subpath>/`):
top-level `version = 2` (schema version field; a reader hitting an unknown
version MUST fail with a config-style incompatibility error, exit 2 — H7),
`vault_name`, `node`, plus a `[federation]` table (see §13) and `[[file]]`
entries. Per-file fields (canonical order): `id` (64-hex genesis hash),
`genesis` (inline table: `content_sha256`, `original_path`, `ingest_op_id`,
`origin_node`), `sha256` (current content), `path` (vault-relative logical
path), `sync_mode` (`"git" | "manual"` — enum **extensible**, D15: unknown
values are preserved on round-trip, treated as not-git by gc), `size` (bytes),
`created_at` / `updated_at` / `last_scanned` (RFC3339 UTC `Z`). Entries sorted
by `path` byte-wise ascending (mirror lock canonical form). Include a verbatim
sample block.

**§10 WAL entry schema + hash-chain rule** (`meta/wal/` under the vault root):
one TOML file per entry, named `<seq>-<op_id>.toml` (seq zero-padded to 12
digits — append = atomic `Put` of a new key, never an in-place edit). Fields:
`seq`, `op_id` (UUIDv4 hex), `prev_hash` (64-hex sha256 of the canonical bytes
of the previous entry; genesis entry uses 64 zeros), `op_type`
(`ingest | move | delete | sync_mode | gc | roster | scan`), `args` (op-typed
table), `blob_refs` ([]string — file IDs the op locks), `state`
(`intent | done | failed`), `actor` (whois identity), `created_at`,
`updated_at`. **Hash-chain rule (normative):** each entry's hash is
sha256 over its canonical serialized bytes *excluding* nothing — `prev_hash`
links entries; any reader replaying the chain MUST verify every link and fail
on a break (tamper-evident, D17). State transitions happen by writing a
sibling `*.done` / `*.failed` marker file (the intent entry is immutable so
the chain never re-hashes).

**§11 Genesis record + file-ID derivation:** `id = sha256(canonical genesis
record bytes)` where the record is
`{content_sha256, original_path, ingest_op_id, origin_node}` serialized in
that fixed field order (canonical TOML, LF line endings — byte-exact
determinism is load-bearing). Properties to state: unique (op id + path salt),
location-independent, regeneratable from the record, **self-certifying**
(sha256(record)==id proves the record). Short display form = first **12 hex**
chars. The ID is NOT the content hash (manual files drift — H12/D19).

**§12 Pull receipt format:** `~/.tailvault/receipts/<id>.toml` —
`id`, `genesis` (full record), `path`, `sha256_at_pull`, `pulled_at`,
`source_node`. Written by every `vault get` (D24b); read by
`restore-identity` (Block 4).

**§13 `[federation]` roster section** (inside the catalog): `fed_id`
(64-hex, minted at `fed init` as sha256 of the init op's genesis WAL entry),
`[[federation.member]]` with `name`, `node`, `joined_at`, `status`
(`active | left | evicted`). Leave/evict keep the row with a status change
(history matters for WARN messages, D28).

**§14 Client cache format:** `~/.tailvault/cache/fed-<fed_id>/current.toml` +
`previous.toml` — snapshot of roster, per-member catalog summaries (file
count, ids, last seen), reachability, `taken_at`. On every successful
federation read the client rotates current→previous and writes a new current.
**Advisory only; live pings always win** (D26).

**§15 `TV-FED-*` codes + exit bucket 6:** extend the §5 catalogue table:
`TV-FED-01` partial view — object not found among reachable members and ≥1
member unreachable ("cannot prove absence"); `TV-FED-02` federation op needs
ALL members and ≥1 was unreachable (gc gate, D27/R3); `TV-FED-03` WAL
hash-chain verification failed (tamper/corruption). All map to new **exit
bucket 6** (federation/partial view), added to the §5 exit table. Restate the
resolution semantics: found-at-home = success; found-elsewhere = success +
WARN (heal); not-found + ≥1 unreachable = TV-FED-01; not-found + all
reachable + no pending move = TV-OBJ-01.

**§16 Password hash file + auth error code:** `meta/auth/passwd` under the
vault root — a single line
`argon2id$v=19$m=65536,t=3,p=4$<salt-b64>$<hash-b64>` (PHC string format via
`golang.org/x/crypto/argon2`). Mode `0600`. No recovery: reset requires
SSH/physical access (D9, H8). Required for mutating remote ops only; **reads
are never password-gated** (they ride tailnet ACL + SSH alone) — state this
rule normatively. Define **`TV-AUTH-01`** — password missing/rejected on a
mutating remote op (mv, rm, sync-mode change, remote gc, evict, roster
writes); *Fix:* re-run with the correct password, or reset the hash over
SSH/physical access. Exit bucket: **2** (precondition/auth — reuse, no new
bucket; the op was refused before any work, exactly like a config
precondition). Add the row to the §5/§15 catalogue tables. **Explicit
ruling:** `fed join` (and every other roster update — leave applied remotely,
evict) writes the `[federation]` section of each member's catalog and is
therefore a **mutating op on each member: it IS password-gated** per the
default rule — each member's own password authorizes the roster write on
that member; pending roster ops queued for unreachable members carry the
same requirement when retried. State this so Block 4's `fed` tasks can cite
it directly.

Also add a short **"v2 frozen Go API names"** table (extending §8) reserving:
`catalog.Catalog`/`catalog.File`, `wal.Entry`/`wal.Log`, `identity.Genesis`/
`identity.MintID`/`identity.Receipt`, `fed.Roster`/`fed.Member`/`fed.Snapshot`
— filled in detail by Tasks 28–31 but named here so workstreams don't guess.

### EDGE-CASES.md

- **File:** `EDGE-CASES.md`
- **Action:** create
- **Purpose:** the Block 7 running log skeleton (D31).

```markdown
# EDGE-CASES.md — running log (Blocks 3–6)

> Append-only. Every dev/QA notes edge cases discovered while building
> the implementation blocks: what was chosen, what was punted, what worked.
> The edge-case design block (task 56) consumes this log. Entry format
> below; never delete entries.

## Entry template
- **Date / Task:** …
- **Edge case:** …
- **Decision:** chose | punted | worked-around …
- **Follow-up:** none | GH issue | Block 7 candidate
```

Implementation Notes:

- v1 sections §1–§8 are untouched; v2 is purely additive (D29: no migration
  machinery, no real v1 vaults exist).
- Keep every timestamp rule, size-unit rule (§7 binary units), and canonical
  ordering convention identical to v1 — call this out explicitly in §9.
- Lock schema v2 changes are specified in **Task 35**, not here, but §11 must
  state that lock entries will embed the full genesis record (D24a) so Task 35
  has a citation.

## Implementation Checklist

- [ ] §9 catalog schema + verbatim sample + schema-version incompatibility rule.
- [ ] §10 WAL entry schema, file-per-entry layout, hash-chain rule, state markers.
- [ ] §11 genesis record canonical serialization + ID derivation + 12-hex short form.
- [ ] §12 pull receipt format.
- [ ] §13 `[federation]` roster section + member status lifecycle.
- [ ] §14 client cache format (current+previous, advisory).
- [ ] §15 `TV-FED-01/02/03` + exit bucket 6 + resolution semantics table.
- [ ] §16 argon2id password file format + perms; `TV-AUTH-01` (exit bucket 2);
  reads-never-gated rule; explicit `fed join`/roster-writes-are-gated ruling.
- [ ] v2 frozen API-name table appended to/alongside §8.
- [ ] `EDGE-CASES.md` skeleton created at repo root.

## Testing Requirements

No Go tests (docs-only). Review-level checks instead:

- Every sample block parses as valid TOML (spot-check with a TOML linter).
- Cross-references resolve: §15 ↔ §5 exit table, §11 ↔ Task 35, §13 ↔ §14.
- No contradiction with proposal Part II or D1–D31 (diff against the
  brainstorm decision list).

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- `SPEC.md` gains §9–§16 covering all the frozen artifacts above (catalog,
  WAL, identity, receipts, ignore file, roster, caches, TV-FED codes,
  password file + TV-AUTH-01 + the roster-writes-gated ruling); v1 sections
  unchanged.
- Every format detail another Block 3 task needs (field names, file locations
  on the node, canonical ordering, error codes, exit bucket) is answerable by
  citing a v2 section.
- `EDGE-CASES.md` exists with the entry template.

## Related Proposal Sections

> 3.1 **SPEC v2 freeze**: catalog schema, WAL entry + hash-chain, genesis
> record / file-ID, pull receipts, `.tailvaultignore`, `[federation]` roster,
> client cache format, `TV-FED-*` codes + exit 6, password hash file.
> (Everything else cites this.)

> Every storage node keeps its own hash-chained WAL (each entry embeds the
> hash of the previous entry → tamper-evident, no consensus needed).

> `EDGE-CASES.md` is a running log: every dev/QA appends edge cases discovered
> while building Blocks 3–6 … Block 7's design consumes that log.

## Notes & Considerations

- **Gotcha:** the genesis record's canonical byte serialization must be frozen
  to the byte — a whitespace difference changes every file ID in existence.
  Specify it exactly (field order, quoting, LF) and include a worked example
  (record → id hex) others can use as a test vector.
- **Gotcha:** §10's "immutable intent + sibling state markers" exists so the
  hash chain never re-hashes on state change; don't "simplify" to in-place
  edits.
- **For Next Task:** Task 28 implements §9 byte-for-byte; its fixtures are the
  §9 sample block.
- Also include `.tailvaultignore` semantics in §9 or a short §9b: repo-root
  (vault-root) file, gitignore-style doublestar globs, opt-out only, overridden
  by explicit `track` (D22).
- **Prev:** [task-25-tests-docs-ci](./task-25-tests-docs-ci.md) ·
  **Next:** [task-28-catalog](./task-28-catalog.md)
