# tailvault — Design & Planning

> **Status: PLANNING ONLY.** No implementation lives here yet. This document
> captures the requirements, decisions, and architecture worked out in
> discussion so the design can be poked at before any code is written.

---

## 1. What it is (one paragraph)

`tailvault` is a small CLI tool for storing large binary files **outside** a git
repo while keeping them in sync with `git push` / `git pull`. Bytes physically
live in a **content-addressed folder on a Tailscale node** (reached over the
tailnet); the git repo only carries small pointer + lock files. It is a
purpose-built, Tailscale-aware alternative to Git LFS, with retention and
versioning defaults tuned to **prevent repo/storage bloat**. It can also be used
standalone (outside git) via the CLI, with a possible app later.

Motivating case: the `root-pnp` project in this workspace is ~1.1 GB of
print-and-play PDFs/STLs that bloat git history. tailvault would keep the laptop
clone lean and park the blobs on a home Tailscale node.

---

## 2. Hard requirements (from discussion)

- **Per-project config**, committed into the repo so every clone agrees.
- **User-level registry** of storage locations (named tailnode targets), kept in
  the CLI config, not the repo.
- Locations addressed by **tailnode (MagicDNS name or 100.x IP) + path**.
- **Multiple locations per node**; storage may use **child folders** in the FS.
- **Relocatable**: move storage to a different tailnode by repointing config.
- **Hard-fail semantics**: if the target node is down, or an expected object is
  missing, the operation **fails** — never a silent success.
- **Syncs on git push/pull** (and works standalone with no git).
- **Filters**: a size threshold ("> X MB") plus include/exclude globs decide what
  goes to the vault.
- **Lockfile** records every pushed file: path → content hash, location,
  pushed-at timestamp, and which identity/node pushed it.
- Config **and** lockfile are committed to git to maintain sync.
- **Versioning policy** (see §4): history **off by default** to prevent bloat;
  opt-in per file. Even with history off, **diffs and deletes are tracked**.
- **Retention**: files deleted from git are **auto-deleted** from storage by
  default; per-file `preserve` override. Works at a **per-branch** level.
- The client always **pushes to the host** (host is passive storage).
- **No always-on server** on the storage node beyond Tailscale itself.
- ~~Mobile as a storage host~~ — **dropped** (phones can't reliably serve an
  always-on writable folder; mobile may be an optional mirror only).

---

## 3. Key decisions

### 3.1 Build clean — do **not** wrap Git LFS
LFS's retention model is the opposite of what we want (it keeps every version),
and LFS is git-only so it can't satisfy standalone use. The only things LFS
really provides — the pre-push hook and pointer swap — are **git-native**, not
LFS-exclusive:

- git's `filter` driver (`clean`/`smudge` via `.gitattributes`) swaps the real
  file ↔ a pointer on checkout/commit.
- ordinary git hooks (`pre-push`, `post-merge`, `post-checkout`) drive sync.

So we reuse those *concepts* and skip the LFS binary entirely. Result: exactly
our semantics, standalone mode for free, no upstream to fight.

### 3.2 Content-addressed storage is the core primitive
Store blobs as `objects/<sha256>`. The lock maps **logical path → current sha**.
That single sha is "the ref to the file" (not a commit-linked chain). Every
behavior we want falls out of this one idea — see §4.

### 3.3 Storage backend is swappable (both are "just a path")
- **SSH bare folder** (Tailscale SSH or OS SSH) — most reliable at ~1 GB.
- **Taildrive share** — the "node only runs Tailscale, no git/ssh" model.
Same registry/pointer model either way; the backend is a config detail.

---

## 4. Versioning & retention model

Blobs are content-addressed: `objects/<sha256>`. The committed lock maps each
**logical path → current content sha** (+ location, pushed-at, pusher, flags).

| Event | Behavior |
|---|---|
| **Diff** (content changed) | working sha ≠ lock sha → upload new blob, repoint the ref; if history **off**, GC the old sha |
| **Move / rename** | same sha → just rename the key in the lock, **zero transfer** |
| **Delete** (removed from git tree) | drop the mapping; GC the blob unless `preserve` is set |
| **History ON** (opt-in per file) | instead of GC-ing the old sha, append it to a per-file ref list so revert can repoint to an older sha |

**Defaults:**
- `history = false` for every large file (anti-bloat). Only the current blob is
  kept; old blobs are GC'd on diff. **Revert is therefore opt-in per file.**
- `auto_delete = true` for every file. Files removed from git are removed from
  storage. Per-file `preserve = true` keeps them.
- New blob is posted **only on a real content diff** — never on a move/rename
  (content-addressing makes this automatic).

**Per-branch:** retention/deletion decisions are evaluated against the branch
being pushed, so deleting a file on one branch doesn't nuke it for another that
still references it. (Exact GC-vs-branches algorithm is an open question — §8.)

---

## 5. Leveraging Tailscale (the tool carries almost no networking/auth code)

| Tailscale feature | What tailvault uses it for | Backend |
|---|---|:--:|
| **MagicDNS names** | stable node addressing in the registry; survives IP changes & moving storage between nodes | all |
| **Tailnet IPs (100.x)** | raw path when pinning an IP is preferred | all |
| **Tailscale SSH** | transport + auth for the SSH backend — no sshd / key management | SSH |
| **Taildrive** (`tailscale drive`) | mount the storage folder as a path; node needs only Tailscale | Folder |
| **Taildrop** (`tailscale file cp`) | lightweight one-shot blob push; push-only, weak for random-access pull + GC | optional |
| **`tailscale status --json`** | pre-flight liveness + node discovery → clean "node down → fail" | all |
| **`tailscale ping`** | confirm a direct path before a large transfer | all |
| **Tailnet ACLs** | who may write to storage — authz we don't have to build | all |
| **`tailscale whois`** | stamp the lock with which identity/node pushed, and when | all |
| **`tailscale serve`** | (future app) a TLS object API inside the tailnet if we outgrow folders | future |
| **Funnel** | deliberately unused — tailnet-only by design | — |

**Net:** addressing, transport, liveness, authz, and identity-stamping are all
Tailscale primitives. tailvault owns only: the config/lock formats, the
content-addressed store + GC, the git filter/hook glue, and the
retention/branch policy.

---

## 6. Proposed surfaces (sketch — not final)

### 6.1 Files committed to the repo
- `tailvault.toml` — project config: backend choice, location ref, size threshold,
  include/exclude globs, per-file/per-pattern `history` and `preserve` flags.
- `tailvault.lock` — path → current sha, location, pushed-at, pusher; the source of
  truth for what's stored and when.
- `.gitattributes` — registers the `clean`/`smudge` filter for tracked paths.

### 6.2 User-level config (NOT in the repo)
- `~/.config/tailvault/locations.toml` — named storage locations:
  `name → { node: <magicdns-or-ip>, base_path, backend: ssh|taildrive }`.
  Supports multiple locations per node.

### 6.3 Storage layout on the node
```
<base_path>/
  objects/<sha256>            # content-addressed blobs (deduped)
  refs/<path-id>              # (history-on files only) list of prior shas
  meta/                       # optional: per-location manifest / GC bookkeeping
```

### 6.4 CLI verbs (sketch)
```
tailvault init                 # write tailvault.toml + .gitattributes
tailvault location add <name>  # register a tailnode storage target (user-level)
tailvault track <glob>         # mark paths/patterns as vault-managed
tailvault status               # what's local-only / pushed / drifted / orphaned
tailvault push [--branch b]    # upload diffs, GC deletes, update lock; fail if node down
tailvault pull                 # fetch what the current tree/branch needs
tailvault revert <path> <sha>  # (history-on files) repoint to an older blob
tailvault gc                   # prune unreferenced blobs per retention policy
```
Plus git hooks (`pre-push`, `post-merge`, `post-checkout`) that call the same
engine, and standalone use of the same verbs outside a git repo.

---

## 7. Options we evaluated and rejected (for the record)

- **Git LFS (hosted)** — pay-per-quota, retention is opposite of ours, git-only.
- **Git LFS self-hosted server (Forgejo/Gitea, rudolfs, giftless)** — requires
  running a server on the node; rejected.
- **Raw bare repo over SSH / Taildrive, no tool** — works but gives none of the
  filtering/versioning/retention/registry features.
- **Syncthing** — silent divergence/conflict instead of hard-fail; dangerous for
  a live git repo. Rejected.
- **git-annex / DVC** — content sync is decoupled from `git push`, so a green
  push doesn't guarantee bytes landed. Breaks the hard-fail requirement.

Conclusion: none off-the-shelf fit; a **clean custom CLI over a Tailscale folder
backend** is the only thing that satisfies the full list.

---

## 8. Open questions / to resolve before building

1. **Per-branch GC algorithm** — how to decide a blob is safe to delete when
   multiple branches (and remotes) may reference it. Reference-count across
   branch tips? Mark-and-sweep from all refs?
2. **Lock conflict handling** — `tailvault.lock` is committed, so two clients can
   produce merge conflicts. Need a deterministic merge (per-path, last-writer or
   union) or a lock format that merges cleanly.
3. **Backend round 1** — start with SSH (reliable at 1 GB) or Taildrive (the
   "just a folder" aesthetic)? Likely SSH first, Taildrive second.
4. **Pointer format** — what the committed pointer file contains (sha, size,
   location hint) and how `smudge` resolves it without a round-trip per file.
5. **Integrity** — verify blob sha on pull; behavior on corruption/mismatch.
6. **Language/runtime** — Go (ships a static binary, great Tailscale ecosystem)
   vs a script. Leaning Go for a single distributable CLI.
7. **"Always push to host"** — confirm the exact client→host direction for
   deletes and branch-level retention; hosts never initiate.

---

## 9. Future option (tracked, NOT v1): served HTTP object API

> Kept deliberately out of v1, but tracked here so it isn't lost. v1 is the
> **folder model** (SSH / Taildrive) — nothing runs on the node but Tailscale.

### What it means
Run a small HTTP **object API** on the storage node — content-addressed
`GET / HEAD / PUT /objects/<sha256>` — and front it with **`tailscale serve`**,
which gives it a valid TLS cert on the node's MagicDNS name and keeps it
**tailnet-only** (never Funnel/public). This is the bridge from "blobs in a
shared folder" to "blobs behind a private API" — i.e. the phase-two **"app"**
hinted at in the standalone/general-use goal.

### When it earns its keep (adoption triggers)
Only once the folder model becomes the bottleneck. Concretely:
- random-access / range reads of individual objects (folders/WebDAV are clumsy)
- server-side **auth** keyed to tailnet identity (`tailscale whois`)
- server-side **GC coordination**, manifests, integrity verification on read
- a GUI/app frontend that wants a real API, not a mounted path

### Cost
- **Runtime footprint — light.** A Go binary idles ~10–30 MB RAM, I/O-bound under
  transfer; `tailscale serve` adds ~nothing (it rides the already-running
  `tailscaled`). RAM is never the constraint.
- **Build effort — small MVP, moderate to harden.** MVP (`GET/PUT/HEAD`,
  dedup-on-write) ≈ a few hundred lines / a weekend. Hardened (identity auth,
  read-time integrity, GC, concurrent-write safety, range requests) is the bulk
  of the work but stays a contained service.
- **The real cost is operational, not computational:** it reintroduces an
  **always-on daemon** — exactly the "server on the node" v1 deliberately
  avoids. Something to keep alive, restart, and update. The folder model needs
  none of that (writes are on-demand).

### On a constrained host (e.g. a 4 GB Raspberry Pi already running another resident workload, like a "full Hermes")
- **RAM is a non-issue** — `tailscaled` + the object API combined stay well under
  ~100 MB, so it fits in whatever the resident workload leaves free even on 4 GB.
- **Real limits are I/O and crypto, not capacity:**
  - **Storage:** do not park ~1 GB of blobs on microSD (slow random I/O, write
    wear). Use a **USB3 SSD**. Content-addressed = write-once-per-blob, which is
    SD-friendlier, but throughput/longevity still want the SSD.
  - **WireGuard crypto is CPU-bound on ARM** — caps *transfer speed*
    (~150–400 Mbps on a Pi 4, much less on a Pi 3), not viability. A ~1.1 GB sync
    is a few minutes.
  - **Contention** with the resident workload slows syncs during CPU/disk
    spikes; it does not make them fail.
- **Recommendation for any shared/constrained host:** prefer the **folder model
  (SSH bare repo or Taildrive)** — zero always-on weight next to the resident
  workload — and only adopt the served API when its specific features above are
  actually needed.

---

## 10. Next step

Turn this into a formal **design spec**: exact `tailvault.toml` / `tailvault.lock`
schemas, the push/pull/GC algorithms in pseudocode, the storage layout, and the
revert flow — then a task breakdown. No code until the spec is reviewed.
