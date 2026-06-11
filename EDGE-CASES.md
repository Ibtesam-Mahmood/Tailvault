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

- **Date / Task:** 2026-06-11 / task-40 (remote sha256 short-circuit, DEV-C1)
- **Edge case:** `sha256sum` output format varies by implementation — coreutils
  emits `<hex>␠␠<file>`, busybox `<hex>␠*<file>`, and some paths a bare digest.
- **Decision:** chose — trust only the leading 64-lowercase-hex token (`parseSha256Sum`
  + `isLowerHex`); reject empty/truncated/over-long/uppercase/non-hex/prose. Never
  silent-success on garbage output.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-40 (remote sha256 short-circuit, DEV-C1)
- **Edge case:** permission-denied reading a blob over SSH could be misreported as
  a missing object, falsely telling the user the blob is gone.
- **Decision:** chose — classify as TV-NODE-02 (node reachable but not readable),
  same as the write path, NOT TV-OBJ-01. Reserves the missing-object signal for a
  genuine `[ -f ]` miss.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-40 (remote sha256 short-circuit, DEV-C1)
- **Edge case:** `HashObject` of a missing blob must stay behavior-compatible with
  the old stream-and-hash path it replaces.
- **Decision:** chose — a `[ -f ]` miss returns TV-OBJ-01 (exit 5), identical to
  `Get`, so the short-circuit is a drop-in for verify's pass-1.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-46 part 1 (internal/auth — argon2id core)
- **Edge case:** argon2id verify must use the parameters stored IN the hash file,
  not the client's current defaults — otherwise a future param bump (m/t/p) silently
  locks out every node hashed under the old cost.
- **Decision:** chose — `Verify` reads m/t/p + salt from the parsed PHC string and
  derives the candidate with the key length = len(stored hash). `DefaultParams` is
  only ever used when SETTING a new password (DG-27.1, SPEC v2 §16).
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-46 part 1 (internal/auth — argon2id core)
- **Edge case:** "no password set on the node" is operationally different from
  "wrong password" — defaulting an unset node to open would silently weaken D9.
- **Decision:** chose — distinct `ErrNoPassword` sentinel; `MemoryVerifier{Set:false}`
  and (later) the SSH verifier return it, and the command boundary maps it to a
  TV-AUTH-01 telling the user to run `tailvault vault passwd <location>` — mutations
  are REFUSED, never allowed, when no password exists.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-46 part 1 (internal/auth — argon2id core)
- **Edge case:** a script with no TTY and neither TAILVAULT_PASSWORD nor
  --password-file would hang forever on a no-echo prompt — or, worse, a naive impl
  might proceed unauthenticated.
- **Decision:** chose — `ReadPassword` returns `ErrNoPasswordSource` (hard-fail)
  before any network mutation when stdin is not a terminal and no env/file source
  is set. Never a bare `--password` argv flag (visible in `ps`).
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-46 part 1 (internal/auth — argon2id core)
- **Edge case:** PHC base64 fields — padded vs unpadded — must be canonical or two
  nodes disagree on the same hash string.
- **Decision:** chose — `base64.RawStdEncoding` (standard alphabet, `=` stripped)
  for both encode and decode; `ParsePHC` rejects padded input so a non-canonical
  file can't round-trip silently (DG-27.1).
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-46 part 2a (node verify-passwd + Gate)
- **Edge case:** `node verify-passwd` reads the candidate password from stdin — a
  trailing newline could be ambient or a genuine password byte.
- **Decision:** chose — read stdin VERBATIM (no trailing-newline strip). The part-2b
  client SSH verifier writes exactly the password bytes, so an arbitrary password
  (even one ending in whitespace) verifies byte-for-byte.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-46 part 2a (node verify-passwd + Gate)
- **Edge case:** the on-node hash file is corrupt or unreadable during verification.
- **Decision:** chose — refuse with TV-AUTH-01 (exit 2); never fall back to
  client-side verification or a false accept (either would ship the stored hash
  off-node). The hash never leaves the node.
- **Follow-up:** none
