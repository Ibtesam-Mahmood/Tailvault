# Changelog

All notable changes to Tailvault are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/) and versions follow
[Semantic Versioning](https://semver.org/). The [`VERSION`](./VERSION) file is the
**single source of truth**; every task bumps it by `+0.0.1` and adds a matching
`## v<version>` heading here in the same commit.

## v0.0.72 — 2026-06-11

Phase 4 (DEV-46.8) — `gateLocation` gates SSH node-side ONLY. The §16 password gate
verifies node-side ("hash never leaves the node"); a passive taildrive mount can't run
`node verify-passwd`, so taildrive/local mutations are NOT password-gated (rely on the
tailnet-ACL + mount-perm outer layer). Removes the earlier client-side `localVerifier`
(it read the hash off-node, violating §16).

- `internal/cli/vault_auth.go`: `gateLocation` returns nil for non-SSH backends; SSH path
  unchanged (verbatim candidate → exit-0 match / TV-AUTH-01 reject). `localVerifier`/
  `verifierFor` removed. EDGE-CASES.md: taildrive-mutation-ungated + task-51 threat-model note.

## v0.0.71 — 2026-06-11

Phase 4 (task-48 engine) — restore-identity core. `ingest.RestoreIdentity`
re-establishes a file's genesis identity from a trusted receipt / record / lock
source, logged as a `restore` WAL op. Self-contained engine (the `vault
restore-identity` command wiring follows).

- `internal/ingest/restore.go` + test; `internal/identity` gains `VerifyID` +
  `ReadReceiptFile`/`ReadRecordFile`; `internal/wal` adds `OpRestore` ("restore");
  SPEC §10 op_type list += `restore` (mandated by task-48, like OpPasswd/DEV-46.6).

## v0.0.70 — 2026-06-11

Phase 4 (task-46 enforcement seam) — shared location resolver + auth gate for the
mutating commands. The single helper the gated ops (mv/rm/sync_mode/etc.) call;
unblocks the task-49 `track` command (which reuses `locationBackend`).

- `internal/cli/vault_common.go`: `locationBackend(name)` (location-name→backend
  resolver, reused by 43/44/45/49).
- `internal/cli/vault_auth.go`: `gateLocation`/`verifierFor`/`localVerifier` — the
  password-gate helper the enumerated §16 destructive/move/roster ops call. Per
  DEV-46.7, ingestion (put/track) and reads do NOT call it.

## v0.0.69 — 2026-06-11

Docs (DEV-46.7 ruling) — record the ingestion-gating ruling durably in EDGE-CASES.md
for the threat-model block (task-51). `vault put` / `track` are NOT password-gated:
the frozen §16 gated set is enumerative and excludes ingestion, and task-46's audit
pins to "§16 list exactly" — so gating ingestion would be an unmandated frozen-SPEC
amendment. Captures the threat-model follow-up (ungated ingestion ⇒ any tailnet+SSH
peer can add/overwrite content without the password; task-51 to assess for a future
SPEC rev). Endorsed by maintainer. No source change.

## v0.0.68 — 2026-06-11

Phase 4 (task-49 engine) — `ingest.Track`: the backend-based manual-ingest
registration engine. HashObject → genesis → WAL catch-up → catalog (via
`PutOverwrite`) → done; idempotent, drift-aware, resumable. Self-contained engine
(no caller yet); the `track` command wiring follows once the location-name→backend
resolver (task-41) + lock-v2 (task-35, for `--lock`) are available.

- `internal/ingest/track.go` + test. No interface changes.

## v0.0.67 — 2026-06-11

Phase 4 (task-41) — `vault ls` + `vault stat` (SPEC v2 §11/§13/§14/§15). The first
federated read commands, attached to the `vault` group (init, scan, **stat, ls**).

- `vault stat <path|id>`: resolve via `fed.Resolver` (BackendQuerier + member probe)
  → exact §15 boundary map (PartialView→TV-FED-01 exit 6, Missing→TV-OBJ-01 exit 5,
  FoundElsewhere→success+heal WARN, ErrChainBroken→TV-FED-03 exit 6); prints id/path/
  home/sync_mode/size/sha/timestamps + reachability; `--check` (HashObject on home →
  fresh/drifted, never "corrupt" — H12), `--json`.
- `vault ls [loc[/path]]`: fan out roster.Active() via probe, read each reachable
  member's catalog; offline members backfilled from advisory cache (marked "cached",
  never live, D26); empty path-result with ≥1 member unreachable → TV-FED-01 (exit 6).
- Shared `vault_common.go` seams (backendForRegistry/memberProbe/resolveOutcome/
  parseTarget/loadRoster/readCatalog) reused by tasks 42/44/45. Reads NEVER
  password-gated (§16). 5 DEVIATIONS + 3 EDGE-CASES (see PR).

## v0.0.66 — 2026-06-11

Phase 4 (SG-6 gc test coverage) — adds `TestPersistCatalogOverBackend_Overwrites`,
a direct replace-wins assertion on gc's catalog write-back through
`backend.PutOverwrite` (the SG-6 gc call-site, migrated in v0.0.65). Test salvaged
from coder-a's parallel a34147c (the gc migration itself was already landed via
coder-b's 0a3b41d); the test is the one non-duplicate part.

## v0.0.65 — 2026-06-11

Phase 4 (SG-6 part 2 — gc call-site) — `vault gc`'s `persistCatalogOverBackend` now
uses `backend.PutOverwrite` (atomic temp+fsync+rename / SSH mv) instead of the
non-atomic Delete-then-Put. Closes the gc call-site of ship-gate SG-6 (atomic on
every backend, local and remote, no special-casing). tasks 33/34 were already clean
(local `catalog.WriteAtomic`); the remaining SG-6 call-site is coder-c's remote
passwd rotation (task-46 part-2b-ii).

## v0.0.64 — 2026-06-11

Phase 4 (task-37 support) — `ingest.ReplayOp` replay seam. Lets `ops retry`
(task-37) reuse bootstrap/scan ordering instead of duplicating it.

- `internal/ingest/replay.go`: replays op_types ingest/scan/move/delete
  (idempotent; MarkDone on success, leaves the WAL entry pending on error for
  MarkFailed); rejects gc/roster/sync_mode (other executors own those). coder-b
  registers it as the task-37 `ops.Executor`. No interface changes.

## v0.0.63 — 2026-06-11

Phase 4 (SG-6 part 1) — atomic-overwrite `Backend` primitive for mutable keys.
Adds `PutOverwrite(ctx, key, r) error` to the `Backend` interface: FSBackend +
Taildrive share `atomicReplace` (temp+fsync+rename, no dedup), SSH does
`cat>tmp && mv` (no dedup probe). The single primitive the SG-6 call-site
migrations route through.

- `internal/backend`: `PutOverwrite` on the interface + all impls; contract test +
  SSH test. The two repo test doubles (wal syncBackend, push forgetfulBackend)
  updated to satisfy the wider interface.
- SG-6 remaining (task #35): migrate gc Delete-then-Put → `PutOverwrite`, gc
  local-root catalog → `WriteAtomic`, and coder-c's remote passwd write → `PutOverwrite`.
  tasks 33/34 need no migration (already overwrite via local `catalog.WriteAtomic`).

## v0.0.62 — 2026-06-11

Phase 4 (task-36) — federated garbage collection. `internal/gc/fed.go`
(`PlanFederated`/`SweepFederated`) + `vault gc` federation wiring.

- gc invariants (hard): only touches `sync_mode=git` objects, skips
  pending-intent blobs (via `wal.Pending`), and requires ALL members reachable
  before sweeping (partial view → refuse, never delete). `-race` clean.
- DEV-36.1: federated entry points named `PlanFederated`/`SweepFederated` (the
  sketch's `Plan`/`Sweep` clashed with the existing v1 `Plan` type / `Sweep` func;
  v1 untouched).
- DEV-36.2 (flagged, Block-5 candidate): catalog overwrite over a backend uses
  Delete-then-Put behind a `PersistCatalog` seam (backend `Put` dedups by key, so
  it can't overwrite) — a non-atomic interim window; a real overwrite primitive is
  a follow-up. Same pattern used by task-33/34 catalog updates (under review).

## v0.0.61 — 2026-06-11

Phase 4 (DEV-46.6) — extend the WAL op_type enum with `passwd` (frozen-SPEC §10
amendment, endorsed by maintainer). Unblocks task-46 part-2b-ii's WAL-logged
password rotation.

- `internal/wal`: `OpPasswd = "passwd"` const.
- SPEC §10: `passwd` added to the op_type list (password-rotation op;
  `blob_refs=["meta/auth/passwd"]`; serialized via WAL-as-lock). Additive — no
  existing op_type changes meaning.

## v0.0.60 — 2026-06-11

Phase 4 (task-46 part 2b-i) — SSH node-side password verifier. Satisfies qa-review's
part-2 forward gate (node-side SSH verification). task-46/#16 still open (part 2b-ii =
`vault passwd` command + WAL-logging + enforcement audit).

- `internal/backend`: `(*SSH).Exec` remote-exec seam (pipes stdin, captures stderr) +
  exported `ShellQuote`.
- `internal/cli/vault_auth.go`: `sshVerifier` (satisfies `auth.Verifier`) runs
  `tailvault node verify-passwd` over SSH, candidate piped VERBATIM, hash never leaves
  the node. Exit 0 = match; TV-AUTH-01 = reject/ErrNoPassword; unreachable → TV-NODE-01.

## v0.0.59 — 2026-06-11

Phase 3 (task-33/34 review fix, SG-3/F4) — wire `wal.ErrChainBroken` →
`tserr.FedChainBrokenErr` (TV-FED-03, exit bucket 6) at the `vault init` (Bootstrap)
and `vault scan` (Diff + Apply) command boundaries, replacing the exit-1 placeholder.
Closes the last WS-A ship-gate — a broken WAL chain now surfaces as the correct
partial-view exit code (hard invariant). `TestVaultChainBrokenIsTVFED03` tampers a
2-entry WAL and asserts both commands return TV-FED-03 + exit 6.

## v0.0.58 — 2026-06-11

Phase 4 (task-39 part — fed.BackendQuerier, pulled forward) — the concrete
`fed.Querier` reading catalog + WAL pending-move over a backend. Federation glue
pulled ahead of the task-39 harness to unblock the Block-4 CLI read chain (task #14
stays open for the harness proper).

- `internal/fed/backend_querier.go`: implements the task-32 `fed.Querier` seam
  against real catalog + wal over a `backend.Backend`. Depends only on
  catalog+wal+backend + the fed engine; `-race` clean (42 PASS subtests).

## v0.0.57 — 2026-06-11

Phase 3 (task-29/34 review fixes) — WS-A ship-gate fix pass closing three qa-review
findings:

- **F2/SG-2:** SPEC §8b API table — `wal.Entry` row no longer lists `State`/`UpdatedAt`
  (DG-27.2: immutable entry; effective state via `wal.Rec`).
- **F3:** SPEC §10 — clarified the chain verifies over RAW on-disk bytes (explicit-encode).
- **F5/SG-5:** `vault scan` now bumps `last_scanned` on every reconciled entry incl.
  clean/`--paranoid`-verified (new `Verified` change kind, freshness-only, no WAL op) + test.

(SG-3/F4 — wal.ErrChainBroken→TV-FED-03 exit-6 boundary wiring — follows now that
task-32's tserr is on the branch.)

## v0.0.56 — 2026-06-11

Phase 3 (task-34) — `vault scan`: reconcile the working tree against the catalog.

- `internal/ingest/scan.go` + `internal/cli/vault_scan.go`: detects Clean/New/
  Modified/Moved/Suspect/Deleted, with two hard gates — Suspect entries are NEVER
  auto-applied, Moved preserves the genesis file ID. (The last_scanned-on-clean
  watermark fix / review F5 lands in the following WS-A fix commit.)

## v0.0.55 — 2026-06-11

Phase 3 (task-33) — `vault init` bootstrap. New `vault` command group + `internal/ingest`
bootstrap/ignore.

- `internal/ingest`: `.tailvaultignore` (§9b) matching + deterministic bootstrap that
  scans, ingests, and writes catalog/WAL/genesis with byte-identical resume.
- `internal/cli/vault.go` (newVaultCmd, registered once in root.go alongside node + fed)
  + `vault init`. DG-33.1: SSH remote bootstrap returns TV-CFG "not yet supported"
  (local/taildrive roots work). wal.ErrChainBroken→TV-FED-03 boundary deferred (SG-3/F4).

## v0.0.54 — 2026-06-11

Phase 3 (task-32) — federation resolution engine + `TV-FED-*` error codes (SPEC v2
§15). Rebased onto real wal + identity; exercised against the actual implementations.

- `internal/fed/resolve.go`: fan-out resolution + `moved_to` follow (reachable/down/
  2-hop/cycle), pending-move-blocks-missing, chain-broken propagation, exact reach
  metadata. Pending-move surfaced via an injected Querier seam (DEV-B2: NO compile
  dependency on wal/identity — clean layering; concrete Querier lands as the
  pulled-forward fed.BackendQuerier).
- `internal/tserr`: `TV-FED-01/02/03` + **exit bucket 6** (partial-view), coexisting
  with `TV-AUTH-01` (bucket 2). FED-01 exit 6 vs OBJ-01 exit 5 distinction exact.
- 45 PASS subtests across fed+tserr; cache colors never decide resolution.

## v0.0.53 — 2026-06-11

Phase 3 (task-31) — `internal/fed`: federation roster merge, advisory client caches,
reachability accounting (SPEC v2 §13/§14). Rebased onto real wal + identity.

- Roster merge (status lifecycle) reusing catalog.Federation/Member (DEV-B:
  `fed.Member = catalog.Member` alias — no import cycle, no converters).
- Advisory client caches (current/previous, last-seen) — never authoritative for
  resolution decisions. Reachability accounting for per-op all-members-reachable checks.
- `-race` clean across internal/fed.

## v0.0.52 — 2026-06-11

Phase 3 (task-30) — `internal/identity`: genesis-hash file IDs + pull receipts
(SPEC v2 §11/§12).

- Genesis record with EXPLICIT-byte canonical serialization (intentionally
  double-quoted, library-independent, hash-load-bearing) → file-ID derivation;
  §11 test vector reproduced byte-for-byte (`id=30092d830e26…`). 12-hex short form.
- Pull receipts (§12). No SPEC change.

## v0.0.51 — 2026-06-11

Phase 3 (task-29) — `internal/wal`: hash-chained per-node WAL, WAL-as-lock, pruning
(SPEC v2 §10). Rebased onto the task-40 `Backend.HashObject` interface.

- `wal.Entry`/`Log` with `Read`/`AppendIntent`/`MarkDone`/`MarkFailed`/`Pending`/`Prune`;
  immutable intent entries (DG-27.2, no state field — state is marker-derived via `wal.Rec`);
  slot filename `<seq>.toml` with op_id inside (DG-29.1, preserves Put-dedup first-writer-wins).
- Hash chain verifies over RAW on-disk bytes; `wal.Encode` is now EXPLICIT byte construction
  (not `toml.Marshal`) so chain/fed_id hashes cannot drift on a go-toml bump. New frozen
  vector `bb55bed5…93cbc3` (SPEC §10 + testdata).
- **Crash-safety fix (closes review F1 / task #34):** `Prune` uses forward-only anchor markers
  `meta/wal/pruned/<seq>` (put-a-new-key, never delete-then-put), so a crash mid-prune cannot
  brick the chain. New `TestPruneForwardOnlyAnchor`.
- SPEC nits folded: N1 (op_id prose "UUID" not "UUIDv4"), N3 (§13 fed_id vs §11 file-ID clarifier).

## v0.0.50 — 2026-06-11

Phase 4 (task-46 follow-up) — `WriteHashFile` now fsyncs the parent directory after
the rename, so the password file's atomic write survives a crash, matching the
frozen catalog/wal atomicity standard. Closes the optional review nit from
task-46 part 1. Best-effort `fsyncDir` (tolerates platforms that can't sync a dir).

## v0.0.49 — 2026-06-11

Phase 4 (task-46 part 2a of 2) — the wal-independent half of the auth command
surface. task-46/#16 stays open (part 2b = `vault passwd` + SSH Verifier +
enforcement audit, needs wal + the mutating commands).

- `internal/auth/gate.go`: `Gate(ctx, Verifier, ReadOpts)` — password source →
  verify → scrub; returns `ErrWrongPassword`/`ErrNoPassword`/`ErrNoPasswordSource`
  for the command boundary to wrap as TV-AUTH-01. Never calls the verifier when no
  password source exists (test-asserted).
- `internal/cli/node.go`: hidden `tailvault node verify-passwd --vault <base>` (new
  hidden `node` group). Runs on the node over SSH, reads the candidate from stdin
  verbatim, loads the local hash file, exit 0 on match; rejected/none-set/corrupt →
  TV-AUTH-01 (exit 2), never a false accept. The stored hash never leaves the node.
- EDGE-CASES.md: stdin-verbatim password read, corrupt-node-hash → TV-AUTH-01.

## v0.0.48 — 2026-06-11

Phase 4 (task-46 part 1 of 2) — `internal/auth`: argon2id password core (SPEC v2
§16). Dependency-free security-critical crypto half; command wiring + enforcement
audit follow in part 2 (needs wal + the mutating commands). task-46/#16 stays open.

- `internal/auth`: `Derive`/`Verify` via `x/crypto/argon2` (m=65536,t=3,p=4, v19;
  Verify re-derives with params+salt FROM the stored hash, constant-time compare,
  zero-hash never accepts); canonical PHC `FormatPHC`/`ParsePHC` (leading `$`,
  unpadded `RawStdEncoding`, strict reject per DG-27.1); `WriteHashFile`/
  `LoadHashFile` (`meta/auth/passwd`, 0600, temp+fsync+rename atomic); `ReadPassword`
  (--password-file > `TAILVAULT_PASSWORD` env > no-echo TTY; non-TTY+no-source hard
  fail; never a `--password` flag); `Verifier` seam + `MemoryVerifier` for the harness.
- `internal/tserr`: `TV-AUTH-01` (`AuthRequired`/`AuthErr`, exit bucket 2).
- Deps: `golang.org/x/crypto` v0.53.0, `golang.org/x/term` v0.44.0 (direct),
  `golang.org/x/sys` v0.46.0 (indirect) — accepted per D8 (never roll our own crypto).
- EDGE-CASES.md: verify-uses-stored-params, no-password≠wrong, non-TTY-hard-fail,
  unpadded-base64-canonical.

## v0.0.47 — 2026-06-11

Phase 3 (task-28) — `internal/catalog`: parse/validate/canonical-write/atomic-update
for the SPEC v2 §9 catalog (`meta/catalog.toml`).

- `Catalog/File/Genesis/Federation/Member` types in frozen §9 field order; `Parse`/
  `Validate` reject `version != 2` (`ErrIncompatibleVersion`; boundary → exit 2);
  open `sync_mode` enum (D15, never closed-list validated).
- `Canonicalize` (byte-wise Path sort, UTC timestamps) + deterministic `Encode`;
  `Find`/`FindID`/`Upsert`/`Remove`; `WriteAtomic` (temp+fsync+rename+dir fsync) as
  the single write seam, write-ahead ordering doc-commented for tasks 29/33/34.
- SPEC.md §9 sample replaced with the exact `catalog.Encode` canonical output
  (go-toml/v2: single-quoted literals + bare datetimes) so the byte-identical
  round-trip holds literally; added a "Canonical rendering" note. Only the §9
  sample block changed — all other §9–§16 rulings + the genesis test vector intact.

## v0.0.46 — 2026-06-11

Phase 4 (task-40) — remote sha256 short-circuit; resolves accepted deviation
DEV-C1 (GH-2). Promotes `HashObject` from a package-level helper to a first-class
`Backend` interface method so every remote command gets a cheap integrity answer
with no "maybe the backend can hash" branch.

- `internal/backend`: `Backend.HashObject(ctx, key)`; SSH runs `sha256sum` on the
  node and ships only the 64-hex digest (strict `parseSha256Sum`, coreutils +
  busybox tolerant; miss → TV-OBJ-01, permission → TV-NODE-02, ping → TV-NODE-01).
  Taildrive/stub hash locally; `FSBackend.Hashes` counter proves zero-blob-stream.
- `internal/verify`: pass-1 switched to `HashObject` — same corrupt/missing
  reporting, transfer cost drops to a digest.
- EDGE-CASES.md: sha256sum format variance, permission→TV-NODE-02, missing→TV-OBJ-01.

## v0.0.45 — 2026-06-11

Phase 3 (task-27) — freeze SPEC v2 federation contract. Appends "Part 2 —
Federation contract (v2)" to `SPEC.md` (additive; v1 §1–§8 untouched) and creates
`EDGE-CASES.md`. This is the normative base every Blocks 3–4 task cites.

- §9 catalog schema (`meta/catalog.toml` v2, `[[file]]` canonical order, extensible
  `sync_mode`, unknown-version → exit 2) + §9b `.tailvaultignore`.
- §10 WAL entry schema + hash-chain rule (`prev_hash`, 64-zero genesis, immutable
  intent entries + sibling `.done/.failed` markers; fail → TV-FED-03).
- §11 genesis record + byte-exact serialization + file-ID derivation with a
  load-bearing test vector (`id=30092d830e26…`).
- §12 receipts, §13 `[federation]` roster, §14 client caches.
- §15 `TV-FED-01/02/03` + exit bucket 6 (FED-01 exit 6 vs OBJ-01 exit 5); §16
  argon2id PHC password file + `TV-AUTH-01` + reads-never-gated / roster-writes-gated.
- §8b v2 frozen Go API names. Seeds EDGE-CASES.md with DG-27.1/.2/.3.

## v0.0.44 — 2026-06-11

Phase 3–4 planning artifacts — commits the frozen Part II federation plan ahead of
implementation. Lands the Blocks 3–4 task corpus (tasks 27–50 build work plus the
51–59 hardening/dogfooding planning docs), the Block 3 decision log, and refreshes
the phase→block map. No source changes; this is the docs baseline the
`block-3-4/integration` branch is cut from.

- `BRAINSTORM-block-3.md` — federation decision log D1–D31 + holes H1–H12.
- `tasks/task-27`…`task-59` — standalone, normative task files for Blocks 3–7.
- `proposal.md`, `tasks/README.md`, `tasks/task-26-dogfood-root-pnp.md` — Part II
  refresh and 59-task/7-block map.

## v0.0.43 — 2026-06-10

Phase 3 — integration: assert the preserve-deletion fix end-to-end (closes the wave).
Flips the former documented-gap scenario to assert correct behavior and adds the
no-resurrection regression (qa-review check [4]):

- `TestScenario_DeleteAutoDeleteAndPreserve` — deleting both a plain and a `preserve`
  file: the plain blob is swept, the `preserve` blob **survives** (its sha stays in GC's
  keep+preserve set via the `Deleted=true` tombstone).
- `TestScenario_AutoDeleteOff_DeleteKeepsBlob` — an `auto_delete=off` deletion is
  tombstoned and its blob survives (the second survival case).
- `TestScenario_PreserveDelete_NoResurrectionOnPull` — a fresh clone + pull does NOT
  re-create the tombstoned file (the no-resurrection invariant, against pull's `Deleted`
  skip).

Integration-test-only; no production code. Smudge confirmed lock-independent (decodes
the git-fed pointer from stdin, never iterates lock entries; git never smudges a
not-in-tree file), so no tombstone-skip is needed there.

## v0.0.42 — 2026-06-10

Phase 3 — lock merge: same-sha resolution is live-beats-tombstone. In `Merge`'s
same-sha branch, a merged path is `Deleted` only if BOTH sides are tombstones
(`o.Deleted = o.Deleted && e.Deleted`), so a file still live on any branch materializes
on pull instead of staying deleted — removing a nondeterministic merge flicker introduced
by the `Entry.Deleted` tombstone field. Differing-sha resolution unchanged. SPEC §2 merge
note updated. Completes the tombstone merge semantics.

## v0.0.41 — 2026-06-10

Phase 3 — fix(push,pull,status): tombstone deleted-but-preserved entries; never
resurrect them (team-lead's push↔gc ruling, option a). Closes the DESIGN §4 retention
violation the integration suite surfaced: push previously dropped the lock entry for any
deleted file, leaving a `preserve` blob unreferenced for a later `gc` to sweep (silent
data loss).

- **push**: when a deleted file's blob must survive (`preserve` set, or `auto_delete`
  opted out — the exact complement of GC's mark-for-sweep condition `auto_delete && !preserve`)
  keep a `Deleted=true` tombstone instead of dropping the entry, so the sha stays in GC's
  keep/preserve set. Tombstones carry forward across pushes, are skipped as rename sources,
  and a file reappearing at a tombstoned path resurrects into a fresh live entry.
- **pull**: SKIP `Deleted` tombstones — a fresh clone or post-pull does NOT re-create the
  deleted file (blob stays on the node, never fetched). Keeps the blob alive without
  resurrecting the file.
- **status.Classify**: a tombstone yields no row (not orphaned, not live).
- **gc**: no code change (the sha is already covered by keep + preserve sets).

## v0.0.40 — 2026-06-10

Phase 3 — lock: add `Entry.Deleted` tombstone field (additive) to enable the
preserve-aware GC fix. A tombstone keeps a `preserve` blob in GC's keep-set without
materializing the file. `omitempty` keeps live entries byte-identical on disk (no
golden-file churn); `Merge`/`ReferencedSHAs` carry the field through unchanged.
Requested by the push↔gc preserve-deletion data-loss fix. SPEC §2/§8 updated.

## v0.0.39 — 2026-06-10

Phase 3 — docs fix (R-C.25-1): correct `docs/usage.md` min_size unit semantics to
binary (SPEC §7), matching the code — `5MB` = 5,242,880 bytes, `MiB` an accepted
synonym. Docs-only; no code change.

## v0.0.38 — 2026-06-10

Phase 3 — cross-cutting tests, docs, and CI (task-25). Adds a `//go:build integration`
end-to-end suite (`internal/integration/`) covering the 7 proposal scenarios over a
taildrive temp-dir node plus a self-skipping SSH-localhost round-trip; a user-facing
`docs/usage.md` (install → init/setup → track → push/pull → recovery/rollback + error
codes, including both accepted deviation notes); and `.github/workflows/ci.yml`
(build + vet + gofmt-guard + unit + `-tags integration`). No changes to existing code.

## v0.0.37 — 2026-06-10

Phase 3 — document the accepted Taildrive unmounted-share limitation (task-22, R-B deviation).

- Doc-comment on the `Taildrive` backend: the caller must ensure the share is mounted. An
  absent `base_path` hard-fails TV-NODE-01, but an existing-but-unmounted mountpoint is not
  detected (a write would hit local disk). Marker-file/mount-state detection is a recommended
  follow-up. Comment-only, no behaviour change. (team-lead-accepted deviation, condition 1a.)

## v0.0.36 — 2026-06-10

Phase 3 — setup/locations/taildrive hardening (task-10/11/22 fix, R-B).

- `setup` discovery test no longer depends on the host lacking tailscale (a
  `statusForDiscovery` seam forces deterministic manual fallback).
- Taildrive silent-success guard: `preflightNode` now requires a taildrive `base_path`
  to exist as a directory, so an unmounted/absent mountpoint fails TV-NODE-01 instead of
  writing to local disk.
- `resolveBackend` wraps `locations.Load` errors in `tserr.ConfigErr` and guards the ssh
  user / taildrive share; added repo selection-table + `location ls` RunE tests; setup
  Short-help nit.

## v0.0.35 — 2026-06-10

Phase 3 — track pointer-aware report + dead-code removal (task-12 fix, R-A).

- `track` now reports managed files via the pointer-aware `status.ManagedFiles` (replacing
  a local on-disk-size walk), so a min_size-only clean-pointer file is no longer dropped
  from the report. Removed the now-dead `notImplemented` helper (and its `fmt` import) from
  `internal/cli/root.go` — every command is wired.

## v0.0.34 — 2026-06-10

Phase 3 — init/revert exit-code wrapping (task-18/21 fix, R-C).

- `init` (toml write/stat failures) and `revert` (corrupt-lock) now return
  `tserr.ConfigErr` / TV-CFG-01 (exit 2) instead of a generic exit 1. Tests added for
  `init --location` and revert corrupt-lock.

## v0.0.33 — 2026-06-10

Phase 3 — status ManagedFiles pointer-aware size (task-13 fix, R-B C2).

- `status.ManagedFiles` now sizes via `status.ContentSize` (pointer-aware) instead of
  raw `os.Stat`, so a min_size-only file that is currently a clean pointer (~60-byte
  text) is no longer mis-dropped from the managed set during the pre-pull window. Same
  root cause as the v0.0.31 push fix, one layer up. Test `TestManagedFiles_MinSizeOnlyPointer`.

## v0.0.32 — 2026-06-10

Phase 3 — pull corrupt-vs-missing message (task-15 fix, R-B).

- A corrupt/mismatched blob on pull now returns a TV-OBJ-01 error whose cause names
  corruption and whose fix points at `tailvault verify`/re-store, distinct from the
  "missing object" message (still exit 5, still no overwrite).

## v0.0.31 — 2026-06-10

Phase 3 — push records real content size (task-14 fix, R-B).

- Added `status.ContentSize` (pointer-aware: uses `pointer.Decode().Size` when the working
  file is a clean pointer, else `os.Stat`); `push` now sources both `rules.Evaluate` and
  `Entry.Size` from it. Fixes a clean-pointer file (dedup branch) recording the ~60-byte
  pointer text length instead of the real content size (SPEC §2).

## v0.0.30 — 2026-06-10

Phase 3 — lock merge driver (task-24, `internal/lock/merge.go`).

- Added `lock.Merge`, a per-path union 3-way merge (newest `pushed_at` wins on a SHA
  conflict, deterministic tiebreak, `versions[]` unioned, canonical byte-identical
  output), exposed as the hidden `__merge-lock` command and registered as a git merge
  driver by `init`. Covered by a real `git merge` integration test.

## v0.0.29 — 2026-06-10

Phase 3 — `verify` command (task-23, `internal/verify`).

- Added `tailvault verify`: re-hashes blobs and cross-checks the lock — detects corruption
  (digest ≠ key), missing objects (lock SHA absent on the node), and reports orphans;
  history versions included.

## v0.0.28 — 2026-06-10

Phase 3 — `revert` command (task-21, `internal/revert`).

- Added `tailvault revert <path> <sha>`: repoints a history-on file to a recorded prior
  version and stages the lock. History-off / unknown-sha / unknown-path → typed errors;
  already-current is a no-op; a missing blob → TV-OBJ-01 (exit 5); `versions[]` left
  unchanged.

## v0.0.27 — 2026-06-10

Phase 3 — history (task-20, `internal/history` + push hook).

- Added optional per-file history: stable content-independent `PathID`/`RefKey`,
  `AppendVersion` (newest-first, dedup-head) and `ReadVersions`. Wired into `push.Run`
  at the task-20 seam — with history on, a content change appends to `refs/<path-id>`
  and `versions[]` instead of marking the superseded SHA for GC; GC keeps all history
  versions.

## v0.0.26 — 2026-06-10

Phase 3 — git hooks (task-19, `internal/hooks`).

- Added `InstallHooks`: installs pre-push / post-merge / post-checkout hooks (honouring
  `core.hooksPath`), embedding an absolute binary path, forwarding the pre-push exit code,
  idempotent, and warning on a pre-existing foreign hook.

## v0.0.25 — 2026-06-10

Phase 3 — `init` command (task-18, `internal/cli/init.go`).

- Implemented `tailvault init`: writes a `config.Default()` `tailvault.toml`,
  `.gitattributes` filter wiring, git config (filter + the `__merge-lock` merge driver),
  and installs hooks — idempotent, preserving any existing config. Not-a-git-repo
  → `tserr.ConfigErr` (exit 2).

## v0.0.24 — 2026-06-10

Phase 3 — clean/smudge filter (task-17, `internal/filter`).

- Added the git filter engine: `Clean` (byte→pointer, node-free so `git add` works
  offline) and `Smudge` (pointer→bytes, integrity-checked against the SHA). Missing blob
  → TV-OBJ-01 (exit 5); integrity mismatch → exit 5 with no bytes emitted. Hidden
  `filter-clean` / `filter-smudge` commands.

## v0.0.23 — 2026-06-10

Phase 3 — garbage collection (task-16, `internal/gc`).

- Added the mark-and-sweep GC: a pure `PlanSweep`/keep-set core (`BuildKeepSet`,
  `BuildPreserveSet`) and `Sweep`, with a branch-union keep-set assembled via gitglue,
  plus the `gc [--dry-run]` command. History versions and preserved files survive;
  cross-branch references are kept.

## v0.0.22 — 2026-06-10

Phase 2 — config-error wrapping + objMissing fix (WS-B follow-up).

- Config/lock load+parse failures now wrap to `tserr.ConfigErr` (TV-CFG-01, exit 2) at
  the command boundary (`loadConfig`/`loadLockOrEmpty`); leaf packages stay plain-error.
  Pinned by `TestStatus_BadConfig_IsTVCFGExit2`. Removed the interim local TV-CFG helpers
  in favour of the canonical `tserr.ConfigErr` (team-lead mandate).
- Fixed the `ObjMissing` error to strip the `objects/` key prefix (qa-review nit).

## v0.0.21 — 2026-06-10

Phase 2 — pull (task-15, `internal/pull`).

- Added `pull.Run(ctx, root, lk, Deps)` — integrity-checked pull that verifies each
  blob's SHA against the lock before materializing, hard-failing on a missing/mismatched
  object rather than silently succeeding.

## v0.0.20 — 2026-06-10

Phase 2 — push (task-14, `internal/push`).

- Added `push.Run(ctx, root, cfg, lk, Deps, Options)` — the critical-path push with
  fully injectable `Deps` (Backend, Preflight, Whois, GitIdentity, Now), preflight-first
  so an unreachable node fails before any partial write. Leaves a `TODO(task-20, WS-C)`
  seam for history-on version append.

## v0.0.19 — 2026-06-10

Phase 2 — Taildrive backend (task-22, `internal/backend/taildrive.go`).

- Added the `Taildrive` backend (`NewTaildrive(root)`) as a second `Backend` impl
  alongside SSH; passes the shared `RunContract` test.

## v0.0.18 — 2026-06-10

Phase 2 — status (task-13, `internal/status`).

- Added a pure `Classify` + `ScanTree`/`ManagedFiles` and the `status` command
  (`--check-blobs` to probe blob presence on the node).

## v0.0.17 — 2026-06-10

Phase 2 — setup + interactive node discovery (task-11, `internal/setup`).

- Added the `setup` command and interactive `location add` flow: `OnlinePeers` discovery
  over the tailscale fixture, a `Prompter` interface with a stdlib `StdinPrompter`. See
  DEVIATIONS (stdlib prompter chosen over an unlisted TUI dependency).

## v0.0.16 — 2026-06-10

Phase 2 — location registry (task-10, `internal/locations`).

- Added a user-level TOML location registry (XDG) with per-backend validation and a
  reachability `Check` (injected ping), plus the `location add` / `location ls` commands.

## v0.0.15 — 2026-06-10

Phase 2 — `track` command (task-12).

- Implemented `tailvault track <glob>...`: validate-all-before-mutate, append-only
  idempotent `config.AddInclude` + `ValidateGlob`, write-on-change, and an offline
  tree-walk that reports managed files via the rule engine (never contacts a node).
  Routes bad config through `tserr.ConfigErr`/TV-CFG-01 (exit 2).

## v0.0.14 — 2026-06-10

Phase 1 — lock read helpers (pulled into M1 to unblock WS-C).

- Added `lock.Parse`, `lock.Find`, and `lock.ReferencedSHAs` (`internal/lock/query.go`)
  — read-side helpers that gc/verify/revert (WS-C) build on. Updated the SPEC §8 lock row
  to match. Implemented (not stubbed) per team-lead.

## v0.0.13 — 2026-06-10

Phase 1 — config defaults + frozen Go API names (task-01/03 refinement, pulled into M1).

- Added `config.Default() Config`, the canonical zero-config baseline.
- Froze the public Go API names in SPEC §8 (`lock.Lock`/`lock.Entry`, the error-layering
  rule) so WS-B/WS-C build against stable identifiers — resolves the earlier
  `lock.File`-vs-`lock.Lock` naming inconsistency.

## v0.0.12 — 2026-06-10

Phase 1 — config error code (task-12 prereq, pulled into M1).

- Added `tserr.ConfigErr(cause, err)` / `TV-CFG-01` (exit bucket 2) so malformed config
  fails with the spec'd exit code 2 instead of the generic 1. Required by the upcoming
  command wiring (track, init, revert, status, push, pull).

## v0.0.11 — 2026-06-10

Phase 1 — storage backend (task-09, `internal/backend`).

- Added the `Backend` interface (`Stat`/`Get`/`Put`/`Delete`/`List`) with the SSH
  implementation (preflight ping → TV-NODE-01; perm/space → TV-NODE-02; missing object
  → TV-OBJ-01) and `FSBackend`, the in-tree stub all workstreams' tests use. Includes
  `HashObject`, an `ErrNotExist` sentinel, and an exported `RunContract` test helper.
  `Stat` of an absent key returns `Meta{Exists:false}, nil` (existence-as-data, for
  content-addressed dedup); only `Get` of a missing key errors. See DEVIATIONS.

## v0.0.10 — 2026-06-10

Phase 1 — Tailscale wrapper (task-08, `internal/tailscale`).

- Added a thin wrapper over the local `tailscale` CLI: `Client.Status` (peers sorted,
  MagicDNS dots trimmed; missing daemon → TV-NET-01, not-running → TV-NET-02), `Ping`,
  and `Whois`. Exec seam via a `Runner` interface; committed
  `testdata/status.json` fixture so tests need no real node.

## v0.0.9 — 2026-06-10

Phase 1 — pointer files (task-06, `internal/pointer`).

- Added the 4-line pointer format (`tailvault.v1` magic, `key SP value`) with
  `Encode`, `Decode` (strict reject of malformed input), and `IsPointer` sniffing.
  Round-trip and rejection tests.

## v0.0.8 — 2026-06-10

Phase 1 — rule engine (task-05, `internal/rules`).

- Added `rules.Evaluate(cfg, path, size) Decision{Managed,History,Preserve}` —
  `min_size` + include/exclude globs with first-match override precedence over a
  slash-normalized repo-relative path. Tests cover size boundary, include/exclude,
  overrides, and first-match ordering.

## v0.0.7 — 2026-06-10

Phase 1 — `tailvault.lock` state (task-04, `internal/lock`).

- Added `lock.Lock`/`Entry` with `Load`, `Canonicalize` (bytewise path sort, fixed
  field order, `versions` newest-first, RFC3339 UTC `pushed_at`), `Write`, `Upsert`,
  and `Remove`. Tests cover byte-stability, UTC normalization, versions ordering, and
  upsert/remove.

## v0.0.6 — 2026-06-10

Phase 1 — `tailvault.toml` config (task-03, `internal/config`).

- Added `config.Config` (storage + rules incl. per-pattern overrides), `Load`,
  `Validate`, and `Write`, plus `ParseSize` with **binary** units (`5MB` = 5242880;
  IEC synonyms accepted). Table-driven tests for round-trip, size vectors, and
  validation errors.
- Resolves SPEC §7 size-unit binding to binary (see DEVIATIONS).

## v0.0.5 — 2026-06-10

Phase 1 — structured error model (task-07, `internal/tserr`).

- Added `tserr.Error` with stable codes (`TV-NET-01/02`, `TV-NODE-01/02`, `TV-OBJ-01`),
  cause/fix rendering, `Unwrap()`, and `ExitCode()` mapped to buckets (NET→3, NODE→4,
  OBJ→5, default→2). Constructors per condition plus `ExitCodeFor(err)` (nil→0,
  untyped→1, typed→bucket).
- Wired into the CLI: `cli.Execute()` now returns `error`; `main` prints to stderr and
  exits with `tserr.ExitCodeFor(err)`. Commands return typed errors from `RunE`.

## v0.0.4 — 2026-06-10

Phase 0 — Go module + Cobra CLI skeleton (task-02).

- Scaffolded the Go module `github.com/Ibtesam-Mahmood/tailvault` (go 1.26) with
  Cobra v1.8.0: `cmd/tailvault/main.go` entry point, `internal/cli/root.go`
  (`Execute() int`), and one stub command per CLI verb (`setup`, `init`,
  `location add|ls`, `track`, `status`, `push`, `pull`, `gc`, `verify`, `revert`).
- `--version` embeds the `VERSION` file at build time via ldflags
  (`internal/version`); added a `Makefile` (build/test/vet/fmt) and a table-driven
  `root_test.go`.

## v0.0.3 — 2026-06-10

Phase 0 — spec freeze (task-01).

- Added **`SPEC.md`**, the normative frozen contract for Blocks 1–2: `tailvault.toml`
  fields/defaults/validation and rule-eval order (§1); `tailvault.lock` entry fields
  and canonical ordering (§2); the 4-line pointer format (§3); `locations.toml`
  schema and storage layout (§4); the error catalogue (`TV-NET/NODE/OBJ/CFG`) mapped
  to exit buckets 0/2/3/4/5 (§5); resolved open questions Q1–Q10 (§6); and the
  size-unit binding (decimal MB, binary MiB) (§7).
- `CLAUDE.md` planned-structure section now references `SPEC.md`.

## v0.0.2 — 2026-06-10

Spec refinement (no code yet).

- Renamed the repo-committed config/state files to `tailvault.toml` /
  `tailvault.lock` (from `vault.*`) across `proposal.md`, `DESIGN.md`, `CLAUDE.md`,
  and the task backlog.
- Specified **interactive setup + node discovery from the local Tailscale
  session** (`tailscale status --json`, pick-list + manual fallback, no Tailscale
  login or stored credentials); API/OAuth discovery is opt-in and deferred to
  Future. Folded into Phase 1 (`task-01`) and recorded the decision in `issue-01`.
- Specified a **structured error model**: typed conditions with stable codes
  (`TV-NET-*`, `TV-NODE-*`, `TV-OBJ-*`) + bucketed exit codes, preflight-first so
  an unreachable node fails clearly and leaves no partial state. Folded into
  Phase 2 (`task-02`); error-code catalogue added to the Phase 0 freeze.

## v0.0.1 — 2026-06-10

Project bootstrap.

- Imported the frozen design from the planning workspace: `proposal.md` and
  `DESIGN.md`.
- Established project structure, the versioning system (starting at `0.0.1`),
  and the task / issue / PR workflow — see [`CONTRIBUTING.md`](./CONTRIBUTING.md).
- Added project guidance in [`CLAUDE.md`](./CLAUDE.md).
- Seeded the phased implementation backlog (Phases 0–9) as local task files in
  [`tasks/`](./tasks/), with `.github` `Task` / `Issue` templates ready for when
  the backlog is mirrored to GitHub issues (not yet filed — repo is local-only).
