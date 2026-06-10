# Proposal: tailvault — a Tailscale-native large-file store for git

**Status:** Draft · **Date:** 2026-06-10 · **Author:** AI Assistant
**Type:** Feature Addition (greenfield CLI tool) · **Scope:** v1 design blueprint

> **Planning only.** This proposal is the implementation blueprint. No code is
> written yet. It builds on [`DESIGN.md`](./DESIGN.md) and turns it into concrete
> schemas, algorithms, a phased plan, decisions you need to make, and an effort
> estimate.

## Executive Summary

`tailvault` is a single-binary CLI that keeps large binary files **out of git
history** while staying in lockstep with `git push`/`git pull`. Bytes live in a
**content-addressed folder on a Tailscale node**; the repo carries only tiny
pointer + lock files. It is a clean, purpose-built alternative to Git LFS whose
defaults are tuned to **prevent bloat** — no version history unless you opt in,
and storage auto-prunes files you delete from git.

The immediate driver is `root-pnp` in this workspace: ~1.1 GB of print-and-play
PDFs/STLs that bloat every clone. tailvault parks those blobs on a home
Tailscale node (e.g. a Raspberry Pi with a USB3 SSD) and keeps the laptop clone
lean, while a green `git push` *guarantees* the bytes actually landed — or fails
loudly.

The tool deliberately carries almost no networking or auth code: addressing,
transport, liveness, authz, and identity all ride on Tailscale primitives.
tailvault owns only the config/lock formats, the content-addressed store + GC,
the git filter/hook glue, and the retention policy.

---

## Background

### Current State

- No tool exists yet — this is greenfield.
- The workspace is a git repo where each project subfolder is worked by an AI
  agent and committed directly. `root-pnp` alone is **1.1 GB** (PDFs, STLs, 3MF,
  PPTX). That weight is now in git history.
- Git LFS is the obvious incumbent but is rejected (see Alternatives): its
  retention keeps *every* version (opposite of what we want), it is git-only (no
  standalone use), and hosted LFS is pay-per-quota.

### Why This Change?

- Keep clones small and history lean without losing the actual deliverables.
- Self-host the blobs on hardware already on the tailnet — no cloud bill, no
  server to babysit, addressed by stable MagicDNS names.
- A push must be **atomic in spirit**: if the storage node is down or a blob is
  missing, the operation fails rather than silently advancing refs.

### Goals

- Content-addressed blob storage on a Tailscale-reachable folder (SSH or
  Taildrive backend).
- Syncs on `git push`/`pull` via git-native filter + hooks; also usable
  standalone with no git.
- Per-project config + lockfile **committed to the repo**; user-level registry of
  storage locations kept **out** of the repo.
- Interactive setup that registers a storage node by reading the **local Tailscale
  session** (`tailscale status --json`) for a pick-list, or by manual entry — with
  **no Tailscale login or stored credentials**.
- Legible, **structured errors with stable codes + exit codes**; every command
  preflights node reachability and aborts cleanly when the tailnet/node is down.
- Size + glob filtering; per-file/pattern `history` and `preserve` flags.
- History **off by default** (single current ref), but diffs and deletes always
  tracked. Auto-delete **on by default**, with per-file opt-out.
- Hard-fail on unreachable node / missing object. Per-branch retention.

### Non-Goals (v1)

- A served HTTP object API / always-on daemon (tracked as a **future** option in
  `DESIGN.md` §9; v1 is folder-only).
- Mobile devices as storage **hosts** (dropped; optional mirror only).
- Public exposure (Tailscale Funnel) — tailnet-only by design.
- Tailscale-API / OAuth login for **remote** node enumeration (storing an API
  token). v1 reads only the local session; API mode is opt-in and tracked as a
  Future item.
- Multi-writer concurrency beyond a deterministic lock merge (see Open Q2).
- A GUI/app (possible phase two).

---

## Proposed Solution

### Overview

A Go CLI (`tailvault`) wraps three things: (1) a small config/lock layer that
lives in the repo, (2) a content-addressed object store reachable over a
pluggable backend (SSH or Taildrive), and (3) git glue (a `clean`/`smudge`
filter + `pre-push`/`post-merge`/`post-checkout` hooks). The same engine runs
from the CLI directly for non-git use.

### Architecture

```mermaid
graph TB
    subgraph laptop["Laptop (working clone)"]
        WT["Working tree<br/>real large files"]
        PTR["Pointer files<br/>(in git)"]
        CFG["tailvault.toml + tailvault.lock<br/>(in git)"]
        CLI["tailvault CLI + git hooks"]
        REG["~/.config/tailvault/<br/>locations.toml"]
    end
    subgraph node["Tailscale storage node (e.g. Pi + USB3 SSD)"]
        OBJ["objects/&lt;sha256&gt;"]
        REFS["refs/&lt;path-id&gt;<br/>(history-on only)"]
    end
    WT -- "clean filter" --> PTR
    CLI -- "reads" --> CFG
    CLI -- "resolves location" --> REG
    CLI -- "push: PUT blobs / GC" --> OBJ
    CLI -- "pull: GET blobs (smudge)" --> WT
    CLI -. "history-on" .-> REFS
    CLI -- "tailscale status/ping/whois" --> node
    classDef new fill:#99ff99,color:#000
    classDef store fill:#ffeb99,color:#000
    class CLI,REG,PTR new
    class OBJ,REFS store
```

### Detailed Design

#### Repo-committed config — `tailvault.toml`

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

#### Repo-committed state — `tailvault.lock`

The source of truth for "what is stored, where, and when." Committed so every
clone agrees.

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

#### Pointer file (the in-git stand-in)

The `clean` filter replaces a large file's bytes with this on commit; `smudge`
restores real bytes on checkout.

```
tailvault.v1
sha256 9f2b1c…
size 41231873
location home-pi
```

#### User-level registry — `~/.config/tailvault/locations.toml` (NOT in repo)

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

#### Storage layout on the node

```
<base_path>/<subpath>/
  objects/<sha256>     # content-addressed blobs, deduped
  refs/<path-id>       # history-on files only: newest-first list of shas
  meta/manifest.json   # optional bookkeeping (GC marks, integrity log)
```

#### CLI surface

```
tailvault setup                # interactive: register a node by picking from your
                               #   live tailnet (read from the local session) or by
                               #   manual entry, then write tailvault.toml + hooks
tailvault init                 # non-interactive: write tailvault.toml + .gitattributes, install hooks
tailvault location add <name>  # register a tailnode target (writes locations.toml);
                               #   --node to set manually, omit to pick from the tailnet
tailvault location ls          # list registered locations + live reachability
tailvault track <glob>         # add include rule(s) to tailvault.toml
tailvault status               # local-only / pushed / drifted / orphaned
tailvault push [--branch b]    # upload diffs, GC deletes, update lock; fail if node down
tailvault pull                 # fetch blobs the current tree/branch needs
tailvault revert <path> <sha>  # history-on files: repoint to an older blob
tailvault gc [--dry-run]       # prune unreferenced blobs per retention policy
tailvault verify               # re-hash stored blobs; report corruption/missing
```

#### Backend abstraction

```go
// Every backend is "a path that can hold objects/ and refs/".
type Backend interface {
    Stat(ctx, key string) (Meta, error)     // exists? size?
    Get(ctx, key string, w io.Writer) error
    Put(ctx, key string, r io.Reader) error // content-addressed: skip if Stat hits
    Delete(ctx, key string) error
    List(ctx, prefix string) ([]string, error)
}
// ssh: stream over `ssh user@node` (cat / dd / sha256sum remote-side helpers via stdin).
// taildrive: operate on a locally-mounted share path with os.* calls.
```

#### Push flow (the critical path)

```mermaid
sequenceDiagram
    participant G as git pre-push hook
    participant TV as tailvault push
    participant TS as tailscale
    participant N as storage node
    G->>TV: invoke with pushed refs
    TV->>TS: status/ping target node
    alt node unreachable
        TS-->>TV: down
        TV-->>G: exit 1 (push ABORTS)
    end
    TV->>TV: scan tree for vault-managed files (rules)
    loop each changed file
        TV->>TV: hash content -> sha
        alt sha == lock sha
            TV->>TV: no-op (covers move/rename)
        else new content
            TV->>N: Stat objects/sha
            alt missing
                TV->>N: Put objects/sha
            end
            TV->>TV: update lock entry; if history-off, mark old sha for GC
        end
    end
    TV->>TV: deleted files -> drop entry; mark sha for GC unless preserve
    TV->>N: GC marked shas not referenced by any branch lock
    TV->>TV: write tailvault.lock; `tailscale whois` -> pusher stamp
    TV-->>G: exit 0 -> git proceeds to push refs (incl. updated lock)
```

#### GC / per-branch retention (proposed resolution of Open Q1)

Mark-and-sweep keyed on **every local branch's committed `tailvault.lock`**: the
keep-set is the union of `sha256` (and `versions[]` for history-on entries)
across all branch tips. A blob in `objects/` that is in no branch's keep-set and
carries no `preserve` is eligible for deletion. This makes "delete on branch A
doesn't nuke a file branch B still uses" fall out naturally. `--dry-run` lists
what would be removed.

#### Revert flow

For a history-on file, `tailvault revert <path> <sha>` repoints the lock entry's
current `sha256` to the chosen prior version from `refs/<path-id>`, re-smudges
the working file, and commits the lock change. History-off files have no prior
versions to revert to (by design).

#### Tailscale integration points

Per `DESIGN.md` §5: MagicDNS/IP for addressing, Tailscale SSH for the SSH
backend, Taildrive for the folder backend, `tailscale status`/`ping` for
liveness + hard-fail, ACLs for authz, `tailscale whois` for the pusher stamp.
Funnel is never used.

#### Node discovery & location registration (reads the local session)

Registering a storage location should not require typing IPs by hand or handing
tailvault any Tailscale credentials. tailvault reads the **local, already
authenticated Tailscale session** via `tailscale status --json` and offers the
tailnet's nodes as a pick-list:

- `tailvault setup` / `tailvault location add` (interactive): enumerate online
  peers from `tailscale status --json`, let you pick one, prefill its MagicDNS
  name, then prompt for `base_path` and `backend`, and write the entry to
  `~/.config/tailvault/locations.toml`. Manual entry is always available as a
  fallback (`--node <magicdns-or-ip>`), so the flow works even when the daemon
  can't enumerate peers.
- **No Tailscale login or API token is involved.** tailvault never authenticates
  to the Tailscale control plane and never stores Tailscale credentials — it only
  reads the local daemon's existing view of the tailnet. (An optional, opt-in
  Tailscale-API mode for remote enumeration is explicitly **out of scope for v1**
  and tracked under Future.)
- Precondition: Tailscale must be installed and the machine logged into the
  tailnet. If `tailscale` is absent or the local node is logged out, discovery is
  skipped and tailvault falls back to manual entry with a clear message.

#### Error model — fail clearly when the node isn't reachable

Hard-fail is a core guarantee (`DESIGN.md` §2), and the failure must be
*legible*. **Every command that needs the storage node runs a preflight** (
`tailscale status` for tailnet health, then `tailscale ping` / a backend `Stat`
to the specific node) and **aborts before doing any partial work** if the node
isn't reachable. Errors are structured, not raw stack traces or SSH noise:

- A small set of typed conditions, each with a stable code, a one-line cause, and
  a concrete next step. Examples:
  - `TV-NET-01 — Tailscale not running.` *Cause:* `tailscaled` not reachable /
    `tailscale` not in PATH. *Fix:* start Tailscale and run `tailscale status`.
  - `TV-NET-02 — Not logged into the tailnet.` *Fix:* `tailscale up`.
  - `TV-NODE-01 — Storage node 'home-pi' is offline/unreachable.` *Cause:* peer
    not in `tailscale status`, or `ping`/`Stat` failed. *Fix:* check the node is
    powered on and connected; `tailvault location ls` shows live reachability.
  - `TV-NODE-02 — Node reachable but base_path not writable.` *Fix:* check the
    SSH user / Taildrive share and `base_path` permissions.
  - `TV-OBJ-01 — Expected blob <sha> missing on the node.` (integrity/`pull`).
- **Exit codes** are bucketed so scripts and the git hooks can branch on them:
  `0` success; `2` config/precondition (bad `tailvault.toml`, no location); `3`
  network/Tailscale down; `4` node unreachable; `5` integrity/missing blob. The
  `pre-push` hook surfaces the same code so a failed push reads obviously rather
  than as a generic git error.
- Preflight-first ordering guarantees a node-down failure leaves **no** partial
  upload and an **unadvanced** lock — the repo state never gets ahead of storage.

---

## Implementation Plan

Estimates are in **ideal engineering days** for a solo developer comfortable with
Go and git internals. See the Effort section for calendar conversion and an
MVP-vs-full split.

### Task Breakdown

#### Phase 0 — Decisions & spec freeze [0.5 d]
- [ ] Resolve the Open Questions below (language, backend order, lock-merge, GC).
- [ ] Lock the `tailvault.toml` / `tailvault.lock` / pointer schemas.

#### Phase 1 — Foundation [2 d]
- [ ] Go module + Cobra CLI skeleton; `init`, `location add`, interactive `setup`.
- [ ] Node discovery from the **local session** (`tailscale status --json` parse)
      → pick-list; manual-entry fallback; write `locations.toml`.
- [ ] Config/lock parse + write (TOML); path-id + sha256 hashing.
- [ ] Rule engine: size threshold + include/exclude globs.

#### Phase 2 — Backend layer + SSH [2 d]
- [ ] `Backend` interface; SSH implementation (Stat/Get/Put/Delete/List).
- [ ] `tailscale status/ping` preflight; hard-fail on unreachable.
- [ ] **Structured error model**: typed conditions + stable codes (`TV-NET-*`,
      `TV-NODE-*`, `TV-OBJ-*`) + bucketed exit codes; `location ls` reachability.

#### Phase 3 — Core engine [3 d]
- [ ] `track`, `status`; diff detection vs lock.
- [ ] `push` (upload diffs, dedup-on-Stat, lock update, pusher stamp).
- [ ] `pull` (fetch needed blobs, integrity check).

#### Phase 4 — Retention + GC [2 d]
- [ ] Delete detection; `auto_delete` + `preserve`.
- [ ] Per-branch mark-and-sweep `gc` with `--dry-run`.

#### Phase 5 — Git integration [2 d]
- [ ] `clean`/`smudge` filter driver + pointer format.
- [ ] `pre-push` / `post-merge` / `post-checkout` hooks calling the engine.

#### Phase 6 — History / revert [1.5 d]
- [ ] Opt-in `history`; `refs/<path-id>`; `revert`.

#### Phase 7 — Taildrive backend [1 d]
- [ ] Mounted-path backend; backend selection from `locations.toml`.

#### Phase 8 — Hardening, tests, docs [3 d]
- [ ] `verify`; lock-merge driver; unit + integration tests; README/usage.

#### Phase 9 — Dogfood on root-pnp [1 d]
- [ ] Migrate `root-pnp`'s blobs into a real location; verify lean clone + push.

### Critical Path

```mermaid
gantt
    title tailvault v1 (ideal eng-days)
    dateFormat YYYY-MM-DD
    section Setup
    Decisions & spec      :a0, 2026-06-11, 1d
    Foundation            :a1, after a0, 2d
    section Storage
    Backend + SSH         :a2, after a1, 2d
    Core engine           :a3, after a2, 3d
    Retention + GC        :a4, after a3, 2d
    section Git
    Git integration       :a5, after a4, 2d
    History / revert      :a6, after a5, 2d
    Taildrive backend     :a7, after a6, 1d
    section Ship
    Harden + tests + docs :a8, after a7, 3d
    Dogfood root-pnp      :a9, after a8, 1d
```

### Dependencies

- **External:** Go toolchain; Tailscale installed on laptop + node; `tailscale`
  CLI in PATH; a USB3 SSD on the node for the vault path.
- **Internal:** one decision pass from you (Open Questions) before Phase 1.

### Risk Assessment

| Risk | Impact | Prob. | Mitigation |
|---|---|---|---|
| `tailvault.lock` merge conflicts between clients | M | M | Custom git merge driver (per-path union); single-writer in practice early on (Open Q2) |
| GC deletes a blob another branch/remote needs | H | L | Mark-and-sweep across **all** branch locks + `--dry-run`; never delete `preserve` |
| Taildrive/WebDAV flakiness at ~1 GB | M | M | Ship SSH backend first; Taildrive opt-in (Open Q3) |
| Partial push leaves lock ahead of storage | H | L | Upload blobs **before** writing/committing lock; verify Stat post-Put |
| `smudge` round-trips slow checkouts | M | M | Pointer carries size+location; fetch lazily / batch; cache locally |
| Pi crypto throughput caps transfers | L | M | Documented expectation (few min/GB); not a failure mode |

---

## Testing Strategy

### Unit
- Rule engine: size threshold + include/exclude glob matching (incl. overrides).
- Hashing + pointer round-trip (`clean` → pointer → `smudge` → bytes).
- Lock diff: detect add / content-diff / move-rename (same sha) / delete.

### Integration (against a local SSH "node" and a temp Taildrive-like dir)
- **Hard-fail:** node unreachable → `push` exits non-zero, refs not advanced.
- **Dedup:** re-push unchanged tree → zero `Put` calls.
- **Move/rename:** path change, same content → zero transfer, lock key renamed.
- **Delete + auto_delete:** removing a file prunes its blob; `preserve` keeps it.
- **Per-branch GC:** blob referenced by branch B survives a delete on branch A.
- **History/revert:** opt-in file keeps versions; `revert` restores an old sha.
- **Integrity:** corrupt a stored blob → `pull`/`verify` detects mismatch.

### Manual / acceptance
- [ ] End-to-end on a real Pi over Tailscale with a USB3 SSD.
- [ ] Dogfood: `root-pnp` clone is lean; `git push` lands blobs and fails when
      the Pi is offline.

---

## Distribution & Rollout

- **Build:** `go build` → single static binary; release per-OS (darwin/linux,
  amd64/arm64) via GoReleaser. Optional Homebrew tap.
- **Install on a repo:** `tailvault init` writes `tailvault.toml`, `.gitattributes`,
  and installs hooks; `tailvault location add` registers the node.
- **Phased adoption:** start read-mostly on `root-pnp` behind a branch; verify
  lean clone + reliable push/pull before flipping other projects.
- **Rollback:** the tool is additive — pointers + lock are just files; removing
  the filter/hooks and restoring real files from the vault returns to plain git.

---

## Alternatives Considered

(From `DESIGN.md` §7 — full reasoning there.)
- **Git LFS (hosted / self-hosted):** retention is the opposite of ours; git-only;
  hosted is metered; self-hosted needs a server. Rejected.
- **Raw SSH/Taildrive bare repo, no tool:** no filtering/versioning/retention.
- **Syncthing:** silent divergence instead of hard-fail. Dangerous for git.
- **git-annex / DVC:** content sync decoupled from `git push` — green push ≠ bytes
  landed. Breaks the core guarantee.
- **Do nothing:** 1.1 GB stays in history; every clone pays for it forever.

---

## Open Questions — please resolve before Phase 1

> These are the decisions I need from you. My recommendation is in **bold**.

- [ ] **Q1 — Language/runtime.** Go (single static binary, first-class Tailscale
  ecosystem, easy cross-compile to the Pi) vs a scripted tool (Python/TS).
  **Recommend: Go.**
- [ ] **Q2 — First backend.** SSH (most reliable at ~1 GB) vs Taildrive (the
  "node runs only Tailscale" aesthetic). **Recommend: SSH first, Taildrive in
  Phase 7.**
- [ ] **Q3 — `tailvault.lock` conflict policy.** Custom per-path **union** merge
  driver, vs last-writer-wins, vs declaring single-writer for now. **Recommend:
  ship a per-path union merge driver; assume single active writer early.**
- [ ] **Q4 — GC trigger.** Auto-GC inside every `push` vs an explicit
  `tailvault gc` only. **Recommend: mark on push, sweep on explicit `gc`
  (with `--dry-run`)** — safer, avoids surprise deletes mid-push.
- [ ] **Q5 — Default `min_size`.** What threshold counts as "large"? **Recommend:
  5 MB**, overridable per project.
- [ ] **Q6 — Pointer resolution on checkout.** Eager (fetch all on
  `post-checkout`) vs lazy (fetch on first access). Eager is simpler; lazy keeps
  checkouts instant but needs an access shim. **Recommend: eager for v1, lazy as
  a later option.**
- [ ] **Q7 — Identity stamp source.** Use `tailscale whois` (tailnet identity) for
  the `pusher` field, or git `user.email`? **Recommend: `tailscale whois`, fall
  back to git identity.**
- [ ] **Q8 — Scope of v1.** Ship **full v1** (history/revert + Taildrive
  included) or an **MVP** first (SSH only, no history, no Taildrive) and iterate?
  This drives the timeline below.

---

## Future (tracked, not v1)

- **Served HTTP object API** behind `tailscale serve` — random-access reads,
  identity auth, GC coordination, app frontend. Operational cost = an always-on
  daemon. Full analysis in `DESIGN.md` §9.
- **Lazy/partial checkout**, a GUI/app, and S3-compatible backends.
- **Opt-in Tailscale-API discovery** — enumerate tailnet nodes via the Tailscale
  API (OAuth/API key, token stored in the OS keychain) for machines where the
  local daemon can't, or for remote registration. Off by default; v1 uses the
  local session only.

---

## Appendix

### Glossary
- **Content-addressed:** a blob is stored under the hash of its bytes, so
  identical content dedupes and a move/rename never re-uploads.
- **Pointer file:** the small text stand-in committed to git in place of the
  blob.
- **path-id:** a stable hash of a file's logical path, used to key history refs.
- **Backend:** the transport to the storage folder (SSH or Taildrive).

### File Inventory (to be created during build)
- **Created:** `cmd/tailvault/*`, `internal/{config,lock,store,backend,gitglue,gc}/*`,
  `tailvault.toml`/`tailvault.lock`/`.gitattributes` per repo, `~/.config/tailvault/locations.toml`.
- **Modified:** none (greenfield).
- **Deleted:** none.

### Effort

Ideal engineering days (solo, Go-fluent), summed from the plan:

| Track | Days |
|---|---|
| Setup (Phase 0–1) | 2.5 |
| Storage + core (Phase 2–4) | 7 |
| Git + history + Taildrive (Phase 5–7) | 4.5 |
| Harden + tests + dogfood (Phase 8–9) | 4 |
| **Total (full v1)** | **~18 ideal eng-days** |

**Calendar conversion** (ideal days run ~60–70% efficient once meetings, context
switches, and unknowns are included):

- **MVP** (SSH backend; `init/track/status/push/pull/gc`; **no** history, **no**
  Taildrive — Phases 0–5 + light test ≈ 10 ideal days): **~2 weeks full-time**,
  or ~4–5 weeks at part-time/evenings.
- **Full v1** (everything above, ~18 ideal days): **~3.5–4 weeks full-time**, or
  ~7–9 weeks part-time.

These assume the Open Questions are resolved up front and a Pi + USB3 SSD are
ready to test against. The single biggest swing factor is **Q8 (MVP vs full
v1)**; **Q3 (lock-merge)** and **Q1 (Go vs script)** are the next largest.
```
