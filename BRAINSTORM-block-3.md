# Brainstorm — Blocks 3–4: Federated vault layer + remote interaction (working file)

> Scratch document for the new-feature brainstorm. **Not normative.**
> `proposal.md`, `SPEC.md`, and `tasks/` are untouched until we agree to apply.
> End state: user manually folds this into the proposal, then tasks get cut.

Status: **OPEN — semantics largely locked; ingestion detail + a few UX
questions remain**
Updated: 2026-06-10

## New identity (D10 — replaces the old one-liner)

**A distributed Dropbox-style storage system with first-class native git
support, built on Tailscale plus custom logic, protocols, processes, and
security.** Git LFS-replacement is now one native feature, not the mission.

## Restructure (agreed)

- Block 3: vault catalog + federation/resolution (per-node WAL, logical
  layer, lock healing, partial-view semantics)
- Block 4: remote interaction CLI + ingestion + moves + sync-mode management
- Block 5: security analysis of the entire system (dedicated block)
- Block 6: dogfood — 2+ real nodes; federation + security + per-command
  guided acceptance; programmatic/demo tests automated by dev/QA.

## Decisions

- D1 ✅ Both features; blocks restructured.
- D2 ✅ Block 3 = catalog+federation; Block 4 = remote CLI+moves.
- D3 ✅ Single-home blobs; redundancy deferred (GH-3).
- D4 ✅ Non-git files native (superseded by D10's full identity statement).
- D5 ✅ Pull warns; `tailvault heal` is a separate explicit command.
- D6 ✅ Operation journal = **per-node WAL** (user-refined): each storage
  node stores its own write-ahead log; the federated layer itself is
  STATELESS — every node reports its own state when pinged, and an operation
  is allowed/blocked/unavailable based on the WAL+state of exactly the nodes
  whose resources (config, storage, state) the op references.
- D7 ✅ Resolution = fan-out query + `moved_to` records as forwarding
  pointers.
- D8 ✅ Reuse built transport/crypto only — never roll our own.
- D9 ✅ Per-node password (argon2id hash on node, no recovery — reset
  requires SSH/physical access); required for mutating remote ops only
  (mv, rm, sync-mode change, remote gc); reads ride tailnet ACL + SSH.
- D10 ✅ Identity statement above.
- D11 ✅ Move transport = **node-to-node SSH/rsync over the tailnet**
  (WireGuard-secured). Taildrop = rejected alternative (inbox/staging
  delivery, not path-to-path; extra perms; poor fit) — record in proposal's
  rejected-options.
- D12 ✅ **WAL-as-lock** (answers H3, no root server): in a single-home
  world every op on blob X must touch X's home node, so the home node's WAL
  is the natural serializer. Appending the intent record IS acquiring the
  lock — first appender wins; a second op on the same blob sees the pending
  intent and queues or fails with "op in flight". No delay windows, no
  coordinator, no root server. (Per-blob granularity, per R2.)
- D13 ✅ gc consults the home node's WAL: any blob with a pending intent is
  skipped/blocked for that gc run.
- D14 ✅ **gc scope**: gc only ever considers `sync_mode = git` objects.
  Manual/other files are NEVER gc candidates — they're deleted only by
  explicit user action (rm/move). (From user: "make sure gc doesn't touch
  blobs that we don't [own/track via git]".)
- D15 ✅ Sync-mode enum extensible: day-one `git | manual`, schema reserves
  room (`s3`, `watch`, …).
- D16 ✅ H4 error semantics confirmed feasible & adopted (TV-FED codes,
  reachability metadata on every view) — it's error-code logic + accounting,
  fully serverless.
- D17 ✅ (user-confirmed) **Hash-chained WAL adopted; no blockchain.** A tailnet is a small, single-owner, trusted
  network; blockchain pays consensus/replication costs (and violates
  serverless) to solve Byzantine trust between strangers — a problem we
  don't have. ADOPT the one cheap idea from that world instead: **hash-chain
  the WAL** — each journal entry embeds the hash of the previous entry, so
  any tampering with history is detectable on read (tamper-EVIDENT, free,
  no consensus). Feeds Block 5's security analysis.

## Ingestion — 3 ways a manual file enters the federation (D18, agreed)

1. **Manual + track**: user moves/copies a file into a storage folder by
   hand, then runs a `track`-style subcommand on the node (or remotely) to
   register it — this writes catch-up WAL entries for the manual action.
   - User also wants manual moves/deletes inside the vault to be replayed
     into the WAL "via some form of OS hook" → ⚠ tension with serverless,
     see H10. Proposed native mechanism: `tailvault vault scan`
     (reconcile-on-demand: diff disk vs catalog → emit catch-up WAL
     entries). OS-hook/watcher = optional later add-on, daemon-ish.
2. **Creation (vault bootstrap)**: when a storage root is first broadcast
   into the federation, ALL files + subfolders are tracked by default.
   Opt-outs: (a) a local ignore file (overridden by explicit `track`),
   (b) an init-time flag to deselect files interactively.
3. **Push (remote ingest)**: `push` a local file to an active remote storage
   location, choosing the destination path within it. On name conflict:
   prompt — copy / rename / stop. Ownership semantics: after push, the
   **vault copy is the original**; the local source file is now a clone and
   may be safely deleted.

## Operation journal / WAL (D6+D12 consolidated)

- Per-node WAL; federated layer stateless; nodes passive (client-driven
  execution & retries over SSH — R1).
- Dry-run preflight on every mutating op (fail-early before any write).
- Lifecycle: append intent (op id, type, args, blob refs) → receipt →
  execute → confirm → mark done. Hash-chained entries (D17).
- Pending/failed ops surface on any future command that pings the node;
  `tailvault ops` lists, `tailvault ops retry` re-runs; unresolvable →
  flagged for physical fix.
- Blocking: per-blob ordering only (R2). Ops on different blobs independent.
- gc: skips blobs with pending intents (D13); federated gc still requires
  ALL members reachable (R3); only `sync_mode=git` objects are candidates
  (D14).
- Idempotent ops + unique ids; done-entries pruned by journal gc (H9).

## Atomicity standards (adopted)

1. Temp-file + atomic rename (+fsync) for blob, catalog, and WAL writes.
2. Write-ahead ordering: WAL intent → blob bytes → catalog → WAL done-mark;
   crash anywhere = detectable, repairable by verify/heal.
3. Idempotency + op-id dedupe.
4. 3-way verify: lock ↔ catalog ↔ disk.
   (Saga + WAL pattern; no distributed transactions/consensus.)

## Resolution & error semantics (SPEC-bound)

- Found at recorded home → success.
- Found at different member → success + WARN (heal available).
- Not found among reachable, ≥1 unreachable → TV-FED partial-view hard-fail.
- Not found, all reachable, no pending move → TV-OBJ missing.
- Every remote view carries reachability metadata.

## GH issues to file (3)

- GH-1: DEV-B1 taildrive mount-state detection.
- GH-2: DEV-C1 remote sha256 short-circuit — prerequisite for Block 4.
- GH-3: Blob redundancy/mirroring — deferred.

- D19 ✅ **Dual addressing** (answers Q-U4): every federated file gets
  (a) a stable **file ID** — location-independent, survives moves, used for
  all linking/lock/reference purposes — and (b) a **logical path**
  (`<location>/<relative-path>`) used for display, navigation, and selection
  UX. Moves change the path, never the ID.
  ⚠ Consequence (see H12): the ID cannot be the content hash — manual files
  are editable in place, so sha256 changes over a file's life. ID = an
  immutable identifier (e.g. UUID/ULID) minted at ingest, mapped in the
  catalog to {current home, current path, current sha256}; the mapping is
  updated via WAL entries (moves, edits detected by scan).
- D20 ✅ Push conflict prompt gets `--on-conflict=copy|rename|stop` for
  non-interactive use (Q-U2).
- D21 ✅ Default `sync_mode = manual` for all three ingestion paths;
  `git` is only ever set by the git-repo flow (Q-U3).

- D22 ✅ Bootstrap ignore file = `.tailvaultignore`, gitignore-style glob
  patterns (doublestar); overridden by explicit `track`.
- D23 ✅ (H10 resolved) `tailvault vault scan` (on-demand reconcile) is the
  native mechanism for absorbing manual file ops into the WAL. The resident
  OS-hook watcher = optional later add-on → **GH-4** issue with full design
  detail (hook mechanisms per OS: inotify/launchd/systemd-path; opt-in
  per node; explicitly documented deviation from serverless purity).

## File-ID design (Q-U4 follow-up — RECOMMENDATION, needs user confirm)

User asked: can the ID be unique, regeneratable, and not based on storage
location? The honest constraint first — an **impossibility triangle**: an ID
cannot be simultaneously (a) stable across edits AND moves, and
(b) regeneratable from the current file alone — because the only inputs
recoverable from the file itself are its content (changes on edit) and its
path (changes on move). Identity-over-time always needs a recorded birth
fact somewhere.

**Recommended design — genesis-hash ID:**
`id = sha256( genesis-record )` where the genesis record is the file's
ingest WAL entry: { original content sha256, original relative path,
ingest op id, origin node }. Properties:
- **Unique**: two identical files ingested at different paths/times get
  different ids (op id + path salt the hash).
- **Location-independent**: nothing about the CURRENT home is in it; moves
  never touch it.
- **Deterministic / regeneratable**: anyone holding the genesis WAL entry
  (hash-chained, durable, replicated into the catalog) can recompute the id
  byte-for-byte — no random state, no mint counter. NOT regeneratable from
  the file bytes alone (impossible per above); regeneratable from the
  durable birth record, which the WAL already preserves.
- Short display form: first 12 hex chars (like git short SHAs).
If BOTH the WAL and catalog of a node are destroyed, identity is genuinely
lost and re-ingest mints new ids — same failure class as losing a git repo's
history; mitigated by GH-3 redundancy later.

## GH issues to file (now 4)

- GH-1: DEV-B1 taildrive mount-state detection.
- GH-2: DEV-C1 remote sha256 short-circuit — prerequisite for Block 4.
- GH-3: Blob redundancy/mirroring — deferred.
- GH-4: Optional resident watcher (OS hooks → WAL replay) — full design
  detail in the issue (D23).

- D24 ✅ **Identity recovery via replicated genesis records.** The
  genesis-hash ID is self-certifying (`id = sha256(genesis record)`), so any
  surviving copy of the record proves itself. Therefore:
  (a) lock entries referencing federated files embed the full genesis
  record, making every repo clone an off-node identity backup;
  (b) every `vault get` writes a local pull receipt
  (`~/.tailvault/receipts/<id>.toml`) with the genesis record;
  (c) manual recovery command `tailvault vault restore-identity` accepts a
  genesis record from a receipt/lock, verifies sha256(record)==id, and
  re-seeds a rebuilt catalog with the original ID. Always manual — identity
  resurrection is never implicit.

- D25 ✅ Genesis-hash ID design CONFIRMED (D19 + D24). Residual risk
  (never-referenced file on a destroyed node loses identity) accepted;
  closed later by GH-3 redundant recoveries.

## Final pre-proposal rulings (2026-06-11 — all resolved)

- D26 ✅ **Client state caches.** Every client that reads the federated
  layer persists snapshots of the CURRENT and PREVIOUS federation states
  (roster, per-member catalog summaries, reachability) — e.g.
  `~/.tailvault/cache/fed-<id>/`. Caches are ADVISORY, never authoritative:
  used to distinguish "was here before, offline now" vs "never existed"
  (improves H4 partial-view UX), to show last-known state when offline, and
  to detect roster/state changes. Live pings always win.
- D27 ✅ **Per-operation reachability scoping — NO global online
  requirement.** The federation is never "up" or "down" as a whole; truly
  serverless, dynamic, ownership-specific. Each command requires only the
  members whose resources it touches: get/mv/rm → the home node(s);
  ls/search → all members (its scope IS everything); gc → all members
  (its scope is all references — R3 survives as a consequence of this rule,
  not as a special case). fed join/leave updates reachable members
  immediately and queues pending WAL ops for unreachable ones (D6); client
  caches (D26) propagate roster awareness.
- D28 ✅ **Leave = clean detach, not blocked-until-empty** (user design,
  confirmed possible). `fed leave` removes the node from sync: its files
  drop out of the federated tree cleanly; every git repo/sync that
  referenced those files gets a WARNING ("home left the federation —
  repush to a new location or resync from a moved copy"); readers learn via
  state change (D26 caches + fan-out), git repos learn via committed
  lock/config history. Data on the leaver's disk is NOT deleted — it's just
  no longer federated. Caveat (accepted): a node that dies WITHOUT leaving
  just looks offline forever — add manual `tailvault fed evict <member>`
  for declaring a dead member departed (destructive ⇒ password, D9).
- D29 ✅ **No migration path needed** — no real Blocks 1–2 vaults exist;
  any old ones are tests and get recreated. Lock schema v2 (id+genesis) can
  land without v1-tolerance machinery.
- D30 ✅ CLI surface blessed as sketched (minus `vault upgrade`, dropped per
  D29; plus `fed evict` per D28).
- D31 ✅ **Block 7 — edge-case handling** added as the FINAL block, after
  dogfood; designed only after the layers beneath exist. Discipline starting
  NOW: maintain a running `EDGE-CASES.md` log — every dev/QA notes edge
  cases discovered while building Blocks 3–6 (what we chose, what worked,
  what was punted); Block 7's design consumes that log.

## Block map (final)

- Block 3 — vault catalog + federation core
- Block 4 — remote interaction CLI + ingestion + moves
- Block 5 — security analysis & hardening
- Block 6 — dogfood (2+ real nodes, guided acceptance)
- Block 7 — edge-case handling (designed post-implementation from
  EDGE-CASES.md)

## Draft task sketch (preliminary — real task files cut later)

Block 3 (foundation first, then fan-out):
1. SPEC v2 freeze: catalog schema, WAL entry + hash-chain, genesis
   record / file-ID, pull receipts, `.tailvaultignore`, `[federation]`
   roster section, client cache format, TV-FED error codes + exit bucket 6,
   password hash file format. (Everything cites this.)
2. internal/catalog — parse/write/atomic-update; schema version field (H7).
3. internal/wal — append/read, hash-chain verify, op ids, intent lifecycle
   (intent→receipt→execute→done), per-blob blocking, pruning (H9).
4. internal/identity — genesis records, ID mint/recompute/verify, receipts.
5. internal/fed — roster parse/merge, client state caches (D26),
   reachability accounting.
6. Resolution engine — fan-out query, `moved_to` forwarding, partial-view
   semantics (H4), TV-FED errors.
7. `vault init` (bootstrap/broadcast ingestion): track-all default,
   `.tailvaultignore`, interactive deselect flag, resumable via WAL (H11).
8. `vault scan` — disk↔catalog reconcile, catch-up WAL entries, manual-file
   freshness (H12).
9. Lock schema v2 (embed id+genesis), pull WARN on moved/foreign entries,
   `heal` command.
10. gc federation-awareness: pending-intent skip (D13), sync_mode=git
    scoping (D14), all-members reachability gate (D27).
11. `ops` command — list pending/failed, retry, dependency display.
12. 3-way verify (lock↔catalog↔disk) + manual-file edited-vs-corrupt logic.
13. Multi-node integration harness (N stub backends, down-member simulation)
    + Block 3 integration suite.

Block 4:
14. GH-2 prerequisite: remote sha256 short-circuit (lands first).
15. `vault ls|stat` — logical tree + ID display + reachability metadata.
16. `vault get` — download + pull receipt write.
17. `vault put` — remote ingest, `--on-conflict=copy|rename|stop`,
    vault-copy-becomes-original semantics.
18. `vault mv` — intra-location + cross-location (SSH/rsync transport,
    WAL-locked both ends, moved_to record).
19. `vault rm` + sync-mode management command.
20. `vault passwd` + auth enforcement on all mutating remote ops (argon2id).
21. `fed init|join|leave|evict|status` (D27/D28 semantics).
22. `vault restore-identity` (D24).
23. `track` subcommand for manual ingest (path 1).
24. Block 4 integration suite (remote ops against harness, auth cases,
    conflict cases, leave/evict cases).

Block 5 (security): threat-model doc; password/perms audit; WAL chain-verify
tooling; whois-spoofing assumptions; SSH hardening guide; privacy audit
(catalog/receipts leak filenames); govulncheck in CI; fuzz catalog/WAL/
genesis parsers; adversarial review of auth paths.

Block 6 (dogfood): 2+ real nodes; migrate root-pnp; federation walkthrough
(init/join/put/mv/leave); security checks live; per-command guided
acceptance; automated demo-project tests by dev/QA.

Block 7 (edge cases): designed later from EDGE-CASES.md.

## Open questions (need user)

(none — semantics closed; next step is the user's manual proposal write-up,
then task-file cutting)

## Risks / holes — status

- H1 ✅ resolved (D10 identity).
- H2 ✅ resolved (per-node WAL, D6).
- H3 ✅ resolved (D12 WAL-as-lock + D13; no root server needed).
- H4 ✅ confirmed + adopted (D16).
- H5 ✅ resolved (atomicity standards).
- H6 ◐ GH-2 prerequisite for Block 4.
- H7 ⏳ schema version fields + incompatibility error — fold into schema
  tasks.
- H8 ◐ password reset = SSH/physical only (user accepted); Block 5 covers
  hash-file perms, WAL tampering (mitigated by D17 hash-chain), whois
  spoofing.
- H9 ⏳ WAL pruning task — must not be forgotten.
- H10 ⚠ **OS-hook watcher vs serverless**: replaying manual file ops into
  the WAL via OS hooks (inotify/launchd/systemd-path) requires a resident
  watcher — the first daemon-like component in the system. Recommendation:
  ship `vault scan` (on-demand reconcile) as the native answer; treat a
  watcher as an OPTIONAL per-node add-on in a later block, explicitly
  opt-in, documented as a deviation from serverless purity. NEEDS USER
  RULING.
- H12 (new) **Mutable manual files vs content addressing.** The git side is
  immutable (content-addressed blobs); manual files can be edited in place,
  so their sha256 drifts from the catalog until a scan/track re-hashes them.
  Implications: (a) ID ≠ hash (D19); (b) `verify` must distinguish
  "corrupt" from "legitimately edited since last scan" for manual files —
  likely via mtime/size heuristics + a catalog `last_scanned` field;
  (c) remote `get` of a manual file should report hash freshness. Needs a
  task of its own.
- H11 (new) **Bootstrap of huge roots**: "track everything by default" on a
  first broadcast of a big folder tree = hashing/cataloguing everything;
  needs progress UX + resumability (WAL makes resumable natural). Task-level
  concern, noted so it lands in acceptance criteria.

## Next session inputs

Rule on H10; answer Q-U1..Q-U4 → then per-block task sketches (3,4,5,6).
