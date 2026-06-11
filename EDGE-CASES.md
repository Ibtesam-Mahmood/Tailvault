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

- **Date / Task:** 2026-06-11 / task-29 (internal/wal) [review-29 response]
- **Edge case:** WAL entry bytes feed the hash CHAIN + fed_id, so rendering them
  with a TOML marshaler (go-toml/v2) means a future library bump could silently
  change on-disk chain hashes → spurious TV-FED-03 on real data (not just tests).
- **Decision:** chose — `wal.Encode` is now EXPLICIT byte construction (like §11
  genesis), not `toml.Marshal`: fixed field order, LF, double-quoted basic strings,
  bare RFC3339Nano datetime, sorted args keys, args table last/omitted-when-empty.
  Still valid TOML (Decode reads it). New frozen hash vector
  `bb55bed5…93cbc3`; SPEC §10 + testdata updated. (qa-review review-29 gate.)
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-29 (internal/wal) [fix: prune-anchor atomicity]
- **Edge case:** the original `Prune` did `Delete(meta/wal/PRUNED)` then
  `Put(PRUNED)` (Put can't overwrite a dedup key). A crash in that window leaves
  NO anchor while surviving entries start at seq>0 → verifyChain expects genesis
  seq-0/ZeroHash → ErrChainBroken → the WAL is bricked.
- **Decision:** chose — anchors are now **forward-only markers**
  `meta/wal/pruned/<seq>`; the effective anchor is the highest-seq marker.
  Advancing = a single `Put` of a NEW key BEFORE any deletes; the live anchor is
  never deleted before its successor exists, so a crash can never leave the chain
  anchorless. Superseded markers are best-effort cleaned up after the new one is
  durable. (Resolves the QA "prune can brick the WAL" finding.)
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-34 (vault scan)
- **Edge case:** mtime granularity differs by filesystem (1s on some); an edit
  within the same second as the last scan can look unchanged, so the cheap (lazy)
  scan can miss it.
- **Decision:** worked-around — freshness compares size AND mtime>last_scanned;
  the `--paranoid` flag hashes everything (the thorough pass). Lazy scan trades a
  sliver of certainty for speed on big vaults; verify (task-38) is the exhaustive
  check. Documented in Diff.
- **Follow-up:** Block 7 candidate (mtime-equality boundary policy).

- **Date / Task:** 2026-06-11 / task-34 (vault scan) [fix F5: last_scanned watermark]
- **Edge case:** a clean entry that scan HASHED (under --paranoid, or after an
  mtime touch with unchanged content) was skipped without bumping `last_scanned`,
  so the freshness watermark never advanced → redundant re-hashing + weaker Suspect
  detection over time.
- **Decision:** chose — Diff now emits a `Verified` change for hashed-and-matched
  entries; Apply advances `last_scanned` for them (catalog-only freshness bump, NO
  WAL op and `updated_at` untouched — nothing actually changed). Bumped on EVERY
  reconciled entry per the task requirement. New test TestScanParanoidBumpsLastScanned.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-34 (vault scan)
- **Edge case:** a crashed scan leaves orphaned pending intents — scan's catch-up
  ops use random op ids (unlike bootstrap's deterministic ones), so they are not
  resume-deduped and a pending intent can block a re-scan of that blob until
  `ops retry`/clear.
- **Decision:** punted — acceptable: the pending intent is surfaced by `ops`
  (task-37) and retryable; per-blob lock correctly refuses the conflicting re-scan
  rather than corrupting state. (qa-review note.)
- **Follow-up:** Block 7 candidate (scan op-id determinism / auto-recovery).

- **Date / Task:** 2026-06-11 / task-34 (vault scan)
- **Edge case:** ambiguous moves — several files share one content hash, so a
  deleted+added pair cannot be uniquely matched.
- **Decision:** chose — only a UNIQUE (1 deleted, 1 added) hash match becomes a
  Moved (id preserved); ambiguous many-to-* matches fall back to delete+ingest.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-34 (vault scan)
- **Edge case:** Suspect (hash drift, mtime+size unchanged) could be silently
  absorbed, masking corruption.
- **Decision:** chose — Suspect is NEVER applied (returned for reporting); scan
  itself exits 0 if its own work succeeded and prints a loud "run verify" warning.
  The corruption verdict belongs to verify (task-38).
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-33 (vault init bootstrap)
- **Edge case:** byte-identical resume requires stable file IDs and timestamps
  across an interrupted+resumed run, but random op ids / wall-clock timestamps
  differ between runs.
- **Decision:** chose — bootstrap op ids are DERIVED DETERMINISTICALLY from the
  vault-relative path (UUIDv4-formatted but name-based), and catalog row
  timestamps come from the immutable WAL entry's CreatedAt (with an injectable
  clock for tests), so resume replays/reconstructs identical rows. The catalog is
  a projection of the WAL (the recovery record); flushes are batched (default
  N=256) with done markers written only after each batch's catalog flush.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-33 (vault init bootstrap)
- **Edge case:** symlinks in the storage root (cycles / out-of-root escapes).
- **Decision:** chose — BuildPlan skips symlinks entirely (neither followed nor
  ingested). Safe default per the task gotcha.
- **Follow-up:** Block 7 candidate (decide if any symlink policy is wanted).

- **Date / Task:** 2026-06-11 / task-33 (vault init bootstrap)
- **Edge case:** `vault init` walks+hashes the storage root locally, but an SSH
  location has no locally-accessible root; and a wal.ErrChainBroken at the command
  boundary should map to TV-FED-03 (exit 6), whose tserr constructor is owned by
  task-32.
- **Decision:** punted — SSH remote bootstrap returns a clear TV-CFG error
  ("not yet supported; run on a taildrive/local root") (DG-33.1); chain-break
  currently surfaces as the plain wal error (exit 1) until task-32's TV-FED tserr
  is wired in on integration.
- **Follow-up:** GH candidate (SSH remote bootstrap); wire TV-FED-03 mapping at
  integration.

- **Date / Task:** 2026-06-11 / task-30 (internal/identity)
- **Edge case:** the genesis canonical form (§11) is DOUBLE-quoted explicit byte
  construction, while catalog (§9) and WAL (§10) canonical forms are go-toml/v2
  single-quoted. They differ on purpose: genesis bytes feed a cross-implementation
  hash (the file ID) and MUST be library-independent and frozen forever; catalog/
  WAL bytes are produced+consumed by our own encoder. Don't "unify" them.
- **Decision:** chose — identity.CanonicalBytes renders 4 lines explicitly (not
  via a TOML lib); test vector locks id=30092d830e26…
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-29 (internal/wal)
- **Edge case:** WAL-as-lock needs an atomic "claim seq N" primitive, but task-27
  sketched the entry filename as `<seq>-<op_id>.toml` — which puts the op id in the
  NAME, so two racers at the same seq write DIFFERENT keys and backend Put-dedup
  (per-key) cannot arbitrate. The task's own race rule ("Put dedup means first
  write sticks") only works if racers share a key.
- **Decision:** chose — froze the slot filename as `<seq>.toml` with op_id INSIDE
  the file (DG-29.1). The slot file is the lock claim; dedup makes the first writer
  stick; a loser reads back a different op_id and retries at the next seq; a
  same-blob loser is caught by the pending-intent check → ErrOpInFlight. Markers
  keyed by seq (`<seq>.done`/`.failed`). Updated SPEC §10.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-29 (internal/wal)
- **Edge case:** true multi-writer safety needs the backend's Put to be
  create-exclusive (O_EXCL). FSBackend's Stat-then-write is first-writer-wins only
  when calls are serialized; under genuinely parallel access two Puts of one key
  can both rename (last wins). SSH backend (temp+mv) has the same property.
- **Decision:** worked-around — tests serialize backend calls (syncBackend) to
  model one-client-over-SSH; matches the early single-active-writer assumption
  (Q3/D12). A create-exclusive backend Put is the real fix.
- **Follow-up:** Block 5 / GH candidate (backend create-exclusive for N>1 writers).

- **Date / Task:** 2026-06-11 / task-29 (internal/wal)
- **Edge case:** TOCTOU — a key returned by backend.List can be Deleted by a
  concurrent Prune (or loser-cleanup) before Get reads it, surfacing TV-OBJ-01 and
  failing an otherwise-valid read.
- **Decision:** chose — getMaybe treats backend.ErrNotExist on a listed key as
  "skip" (gone), not an error. Verified under `go test -race -count=5`.
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

- **Date / Task:** 2026-06-11 / task-31 (internal/fed)
- **Edge case:** SPEC §8b reserves `fed.Member`/`fed.Roster`/`fed.Snapshot`, but
  §9/§13 make the catalog the serialization home of the roster wire types, and
  catalog is a leaf package (fed→catalog, never the reverse). Two distinct named
  types for one record would force converters across fed/lock/verify.
- **Decision:** chose — catalog owns `catalog.Member`/`catalog.Federation`;
  `internal/fed` declares `type Member = catalog.Member` (alias) to honor §8b
  with zero duplication and no import cycle. Confirmed with coder-a (task-28
  keeps its types unchanged). Logged as DEV-B for the PR DEVIATIONS section.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-31 (internal/fed)
- **Edge case:** the task sketch put a `Roster Roster toml:"-"` field on
  `Snapshot`, but §14's on-disk cache format carries no separate roster section —
  only `fed_id`, `taken_at`, and a `[[member]]` array (name/node/status + summary
  fields). A stored-but-untagged roster field would duplicate the member rows.
- **Decision:** chose — `Snapshot` matches the §14 wire exactly; the roster is
  reconstructed on demand via `(*Snapshot).Roster()` from the member rows
  (joined_at is absent from the cache, so reconstructed members carry a zero
  JoinedAt — acceptable since the cache is advisory and never feeds Merge/fan-out).
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-31 (internal/fed)
- **Edge case:** `Reach.Probe` must not let one hung member stall the whole
  fan-out, but the injected prober might ignore ctx and block forever.
- **Decision:** chose — buffered-channel fan-in that stops collecting on
  `ctx.Done()` and records any not-yet-answered member as Unreachable (bias to
  "cannot confirm"); a leaked hung goroutine drains into the buffered channel and
  exits without wedging the caller. Hard per-member bounds come from passing a
  ctx deadline or a self-timing-out prober (documented on Probe).
- **Follow-up:** Block 7 candidate — revisit whether a default per-member
  timeout should be baked in rather than left to the caller's ctx.
