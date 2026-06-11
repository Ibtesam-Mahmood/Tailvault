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

- **Date / Task:** 2026-06-11 / SG-6 (atomic-overwrite backend primitive)
- **Edge case:** `backend.Put` is create-only (dedups on Stat, no-ops on an
  existing key) — correct for immutable `objects/<sha>`, but mutable keys like
  `meta/catalog.toml` / `meta/auth/passwd` updated in place over the backend were
  handled with Delete-then-Put, which is non-atomic (a crash in the window leaves
  the node with no object).
- **Decision:** chose — added `Backend.PutOverwrite(ctx,key,r)` (interface method,
  so every backend MUST provide it — no silent non-atomic fallback): local backends
  (FSBackend/Taildrive) use a shared temp+fsync+rename `atomicReplace`; SSH uses
  `cat > tmp && mv` (POSIX atomic overwrite); neither dedups. Contract test asserts
  replace-wins for FS+Taildrive; a scripted SSH test asserts no-dedup-probe + tmp+mv.
  Callers persisting mutable keys over the backend (gc/fed PersistCatalog — coder-b)
  migrate to it. (tasks 33/34 already overwrite via local catalog.WriteAtomic/
  os.Rename, so they're unaffected.)
- **Follow-up:** coder-b migrates gc/fed call sites; SSH-remote-catalog path can now
  use PutOverwrite when built.

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
- **Decision:** punted (SSH) + RESOLVED (chain-break mapping, F4/SG-3) — SSH remote
  bootstrap returns a clear TV-CFG error ("not yet supported; run on a taildrive/
  local root") (DG-33.1). The chain-break mapping is now WIRED: vault init/scan map
  wal.ErrChainBroken → tserr.FedChainBrokenErr (TV-FED-03, exit 6) now that task-32's
  tserr is on integration; TestVaultChainBrokenIsTVFED03 asserts exit 6 for both.
- **Follow-up:** GH candidate (SSH remote bootstrap). TV-FED-03 mapping done.

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

- **Date / Task:** 2026-06-11 / task-39 part (fed.BackendQuerier, pulled forward)
- **Edge case:** a roster member that answers a probe but has no catalog yet
  (mid-bootstrap, or backend.ErrNotExist on meta/catalog.toml) — erroring would
  fail a whole federation-wide resolution for one un-catalogued member.
- **Decision:** chose — loadCatalog treats a missing catalog as "holds nothing"
  (nil, no error). Reachability (not catalog presence) is what proves/disproves
  absence; a reachable-but-empty member simply contributes no match.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-36 (federated gc) — CROSS-CUTTING
- **Edge case:** `backend.Put` dedups by key existence on BOTH FSBackend and SSH
  (`if Stat(key).Exists { return nil }`). A single fixed-key file like
  `meta/catalog.toml` therefore CANNOT be overwritten by `Put` alone — a second
  Put is a silent no-op. This affects any catalog UPDATE over a backend (gc's
  post-sweep catalog write, and task-33/34's catalog mutations).
- **Decision:** chose — gc does NOT own catalog-overwrite-over-backend: it takes
  a `FedContext.PersistCatalog` seam injected by the command layer, which owns the
  overwrite mechanism (Delete+Put, or whatever task-33/34 established). Flagged to
  coder-a to confirm their catalog updates don't silently no-op via Encode→Put.
- **Follow-up:** GH/Block-5 candidate — a create-exclusive or explicit-overwrite
  backend primitive for mutable single-key files (catalog) vs append-only objects.

- **Date / Task:** 2026-06-11 / task-36 (federated gc)
- **Edge case:** gate ordering — if the reachability gate ran after candidate
  marking, a partial view could shape a doomed set before the abort.
- **Decision:** chose — strict order in PlanFederated: all-members gate FIRST
  (nothing computed past a failed gate) → git-only candidate scoping (allow-list:
  only sync_mode=="git"; manual/unknown never collectable, D14/D15) → v1 keep-set
  subtraction → pending-intent skip by file id (D13). Sweep re-checks pending at
  execution time and the gc WAL op locks the doomed ids (WAL-as-lock), so a
  concurrent move and gc on one blob serialize. Deletion bias is always "keep".
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-39 part (fed.BackendQuerier, pulled forward)
- **Edge case:** distinguishing a cross-member move (forwarding pointer) from a
  local rename when reading move ops out of a member's WAL.
- **Decision:** chose — BackendQuerier sets MemberView.MovedTo ONLY from a DONE
  move op carrying `args["moved_to"]` (cross-member, per coder-a's task-34/coder-c
  task-44 contract). A local rename (from/to only, no moved_to) is NOT a
  forwarding pointer — resolution must not chase it to another member. Pending
  (intent) move on the id → PendingMove; a still-held id (catalog FindID hit)
  → Found wins even with a pending move (source readable until the move completes).
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-41 (vault ls/stat)
- **Edge case:** `vault ls` of a path that resolves to nothing while ≥1 roster member
  is unreachable — could look like an authoritative empty folder.
- **Decision:** chose — return TV-FED-01 (exit 6), NOT an empty listing. We cannot
  prove the folder is empty when a member's catalog is unreadable (partial view).
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-41 (vault ls/stat)
- **Edge case:** offline members in `vault ls` — show stale data or omit them?
- **Decision:** chose — backfill offline members from the advisory cache, clearly
  marked "cached"/"last seen", never presented as live (D26); offline + no cached
  state → "no cached state". The live probe alone decides reachability.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-41 (vault ls/stat)
- **Edge case:** `vault stat --check` finds a manual-ingest file differs from its
  last-scanned hash.
- **Decision:** chose — report "drifted since last scan", NEVER "corrupt" (H12:
  content-addressed-corruption is verify's verdict, not stat's; manual files drift
  legitimately).
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-43+49 (put/track gating ruling — DEV-46.7)
- **Edge case:** Is INGESTION (`vault put` remote ingest, `track` manual-ingest)
  password-gated? SPEC §16's frozen gated set enumerates exactly {mv, rm, sync_mode
  change, remote gc, evict, roster writes (incl fed join/leave/evict)} — ingestion is
  NOT listed, yet ingestion WRITES to a node's catalog/WAL/blob and `put --on-conflict`
  can overwrite.
- **Decision:** chose — ingestion is NOT password-gated. Follow the frozen §16
  enumerated set exactly (task-46's enforcement audit asserts gated set == §16 list);
  gating ingestion would be an unmandated frozen-SPEC amendment (contrast DEV-46.6, where
  task-46 itself mandated the WAL-logged passwd op). put/track ride the tailnet-ACL + SSH
  outer auth layer like reads; the password gate stays additive for the enumerated
  destructive/move/roster ops only.
- **Follow-up:** ⚠ THREAT-MODEL (task-51): ungated ingestion means ANY tailnet peer with
  SSH access can add OR overwrite content (`put --on-conflict`) on a node WITHOUT the
  password. This is the frozen design's intent (password protects destruction/move/roster,
  not creation) — but task-51 must explicitly assess whether ingestion should join the
  gated set in a future SPEC rev (the SSH/tailnet ACL is then the ONLY barrier to
  unauthorized content writes). Endorsed by team-lead for Block 3–4; deferred to task-51.

- **Date / Task:** 2026-06-11 / task-44/45/46 (taildrive mutation gating — DEV-46.8)
- **Edge case:** The §16 password gate verifies NODE-SIDE ("the hash never leaves the
  node"). SSH can run `node verify-passwd` on the node, but a passive TAILDRIVE mount
  cannot execute code node-side — so node-side password verification is impossible over
  taildrive without reading the hash to the client (which would violate §16).
- **Decision:** chose — `gateLocation` gates SSH node-side ONLY; non-SSH (taildrive/local)
  mutations are NOT password-gated, relying on the outer tailnet-ACL + mount-permission
  layer. Removed the earlier client-side `localVerifier` (it leaked the hash off-node). A
  taildrive backend cannot enforce the §16 password gate by construction.
- **Follow-up:** ⚠ THREAT-MODEL (task-51): mutating ops over a taildrive mount bypass the
  password gate (tailnet ACL + mount perms are the only barrier). Revisit whether
  password-required ops should refuse taildrive backends outright in a future SPEC rev.

- **Date / Task:** 2026-06-11 / task-38 (3-way verify)
- **Edge case:** verify's edited-vs-corrupt verdict must NOT fork from scan's, or the
  two tools would disagree on the same file; and the freshness heuristic needs the
  disk mtime, which the Backend interface does not expose.
- **Decision:** chose — extracted `ingest.ClassifyDrift(entry, diskSize, diskMtime)` as
  the single shared heuristic (scan + verify both call it). ThreeWay's catalog↔disk
  manual-file check is LOCAL-root (os.Stat for size+mtime), matching scan/bootstrap's
  local model (DG-33.1); pending-op suppression runs BEFORE any corruption verdict
  (load-bearing); WAL spot-check = wal.Read (hashes raw on-disk bytes per link, so it
  IS the independent re-derivation — no separate sample).
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-38 (3-way verify) [deferrals]
- **Edge case:** the lock↔catalog id/genesis byte-equality + lock-side
  self-certification need lock-v2 (task-35, embeds genesis in lock entries), which
  isn't on the tip; and the manual-file disk check needs a local root.
- **Decision:** punted — DG-38.1: lock↔catalog cross-check compares sha + presence (v1
  lock fields) now; id/genesis cross-check wires when task-35 lands. DG-38.2: remote
  (SSH) verify sets Options.SkipDisk (WAL/genesis/lock-reconcile still run; manual-file
  disk verify deferred like scan). Local/taildrive run the full check.
- **Follow-up:** wire lock-v2 id/genesis cross-check after task-35; remote manual-file
  disk verify when SSH bootstrap lands.

- **Date / Task:** 2026-06-11 / task-48 (restore-identity gating)
- **Edge case:** restore-identity overwrites the genesis identity (the integrity root) —
  a mutation of existing state strictly more powerful than `mv`; §16 omitted it only
  because task-48 post-dates the gated-set enumeration.
- **Decision:** chose — restore-identity is PASSWORD-GATED (DEV-48.2). §16 amended to
  add it to the enumerated gated set; the task-46 enforcement audit includes it.
  `track`/ingestion stays UNGATED (creates new state, DEV-46.7). task-51 revisits the
  create-vs-mutate line.
- **Follow-up:** task-46 part-2b-ii audit must assert restore-identity in the gated set
  so §16-amended + audit stay consistent.

- **Date / Task:** 2026-06-11 / task-49 (track vault-mode dispatch)
- **Edge case:** `track` is overloaded — Block-1 repo include-rule vs vault registration
  of an existing file.
- **Decision:** chose — dispatch = `--vault`/`--repo` force; else vault only for a
  single registered-location-prefixed arg not inside a repo; ambiguity → demand a flag.
  Block-1 repo behavior byte-for-byte unchanged.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-48 (restore --lock source) [deferral]
- **Edge case:** the restore `--lock` source needs lock-v2 (task-35, embeds genesis in
  lock entries), which isn't on the tip.
- **Decision:** punted — DG-48.1: `--receipt`/`--record` work now; `--lock` errors
  clearly until lock-v2 lands.
- **Follow-up:** wire `--lock` source after task-35.

- **Date / Task:** 2026-06-11 / task-48,49 (resolver convergence)
- **Edge case:** two candidate location resolvers (coder-a's draft
  resolveLocation/gateRemoteMutation vs coder-c's merged locationBackend/gateLocation).
- **Decision:** chose — 48/49 use coder-c's merged `locationBackend`; coder-a's draft
  resolver dropped. One resolver, one gate.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-37 (ops command)
- **Edge case:** `Sweep` must report a chain-broken member without aborting the
  listing, but the task sketch's `([]PendingOp, fed.Reach, error)` tuple has no
  slot for per-member chain-broken state.
- **Decision:** chose — `Sweep` returns `SweepResult{Ops, Reach, Members
  []MemberStatus}` (deviation from the tuple). A chain-broken member's ops are
  WITHHELD (a tampered journal must never drive retries) + surfaced as a
  `MemberStatus{ChainBroken}` trailing row (→ TV-FED-03 class); unreachable
  members degrade the listing, never fail it. `ops list` exits 0 with pending
  shown (inspection); `--fail-pending` gives scripts a non-zero exit.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-37 (ops command — executors)
- **Edge case:** retry must replay through the SAME engine code as the original
  op, but ingest.ReplayOp persists the catalog via LOCAL WriteAtomic (no backend),
  so it cannot replay on an SSH member (no local catalog path).
- **Decision:** chose — replayOpExecutor (ingest/scan/move/delete) is registered
  only for local/taildrive members; on an SSH member `ops retry` of those types
  returns a clear "not yet supported (DG-33.1) — fix on the node" config error,
  consistent with task-33/34's local-root model. The gc executor (mine) uses
  backend.PutOverwrite and so works over ANY backend. Remote-member ingest replay
  lands when SSH bootstrap does.
- **Follow-up:** GH/Block-7 candidate — remote-member ingest-family replay (SSH).

- **Date / Task:** 2026-06-11 / task-42 (vault get — corrupt git blob)
- **Edge case:** `get` of a git-mode file whose stored object doesn't hash to the
  recorded sha.
- **Decision:** chose — hard integrity fail → TV-OBJ (exit 5); the dest-dir temp is
  removed and NO dest file is written (a corrupt download never lands; never-silent-
  success). Streaming tee-hash verifies BEFORE the atomic rename.
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-42 (vault get — drifted manual file)
- **Edge case:** `get` of a manual-mode file that drifted since the last scan
  (stored bytes ≠ recorded sha).
- **Decision:** chose — exit 0, deliver the node's CURRENT bytes (the truth being
  fetched), and print "content has changed since last scan <last_scanned>" (H12 —
  drift on a manual file is legitimate; verify judges corruption, get does not).
- **Follow-up:** none

- **Date / Task:** 2026-06-11 / task-42 (vault get — pull receipt truthfulness)
- **Edge case:** what digest does the pull receipt record, and when is it written?
- **Decision:** chose — receipt written ONLY after the dest rename succeeds (never
  implying possession the user lacks); `SHA256AtPull` = the delivered digest (truthful
  for drifted manual files, equals the recorded sha for git); the receipt's GENESIS
  still self-certifies the id (WriteReceipt refuses a genesis that doesn't mint it).
- **Follow-up:** none
