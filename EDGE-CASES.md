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

## Entries

- **Date / Task:** 2026-06-11 / task-27 (SPEC v2 freeze)
- **Edge case:** task-27 §16 wrote the argon2id password line as
  `argon2id$v=19$...` (no leading `$`), but the PHC string standard — and what
  `x/crypto` consumers interoperate with — is `$argon2id$v=19$...` WITH a leading
  `$` and unpadded base64.
- **Decision:** chose — SPEC v2 §16 freezes the canonical PHC form (leading `$`,
  unpadded base64, 16-byte salt, 32-byte key, m=65536/t=3/p=4). Treated the brief's
  omission as a typo. Logged as DG-27.1 for task-46 (coder-c) + reviewer.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-27 (SPEC v2 freeze)
- **Edge case:** WAL entry lists `state` and `updated_at` fields, but the
  hash-chain requires the entry's on-disk bytes to be immutable (re-hashing on a
  state change would break every downstream `prev_hash`).
- **Decision:** chose — the persisted entry is written once with `state="intent"`
  and `updated_at==created_at`; terminal transitions are recorded only by sibling
  `<seq>-<op_id>.done|.failed` marker files. Effective state = marker-state else
  intent. (DG-27.2; binds task-29.)
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-27 (SPEC v2 freeze)
- **Edge case:** genesis-record canonical form must be byte-exact or every file
  ID changes; "use a TOML encoder" is non-deterministic across libraries.
- **Decision:** chose — froze §11 as explicit byte construction (fixed 4-field
  order, `key = "value"`, TOML basic-string escaping, single LF per line incl.
  last, UTF-8, no BOM) with a load-bearing test vector
  (`…board.pdf` → `30092d830e26…`). task-30 must reproduce it byte-for-byte.
- **Follow-up:** none
