# Tailvault

**A Tailscale-native, content-addressed large-file store that syncs with `git push` / `git pull`.**

`tailvault` keeps large binary files **out of git history** while keeping them in
lockstep with your normal git workflow. The real bytes live in a
content-addressed folder on a **Tailscale node** (e.g. a home Raspberry Pi with a
USB3 SSD); the git repo carries only small **pointer files** and a committed
**lock file**. It is a clean, bloat-averse alternative to Git LFS — **not a
wrapper** — built on git-native filters and hooks.

The headline guarantees:

- **History is off by default** (one current ref per file, no version chain) to
  prevent repo bloat — but diffs and deletes are still tracked, and history is
  opt-in per file.
- **Deletes propagate.** A file removed from git is auto-deleted from storage
  unless you mark it `preserve`.
- **No silent success.** Every command that needs the node runs a preflight and
  **hard-fails loudly** if the node is down or an expected blob is missing — a
  green `git push` means the bytes actually landed on storage.

> **Status — v0.0.104, under active phased development.** The CLI is implemented
> and test-covered through Blocks 0–4 (core workflow + multi-node federation).
> The design is frozen in [`SPEC.md`](./SPEC.md); the build proceeds in phased PR
> blocks. This is a personal project, not yet a tagged stable release. See
> [Status & roadmap](#status--roadmap).

---

## Table of contents

- [The idea in one breath](#the-idea-in-one-breath)
- [How it works](#how-it-works)
- [Features](#features)
- [Requirements](#requirements)
- [Installation](#installation)
- [Updating](#updating)
- [Uninstalling](#uninstalling)
- [Configuration](#configuration)
- [Usage](#usage)
  - [Quick start (repo-managed mode)](#quick-start-repo-managed-mode)
  - [Command reference](#command-reference)
- [Use cases](#use-cases)
- [Error codes & exit codes](#error-codes--exit-codes)
- [Caveats & known issues](#caveats--known-issues)
- [Status & roadmap](#status--roadmap)
- [Documentation](#documentation)
- [Contributing](#contributing)

---

## The idea in one breath

Keep the laptop clone lean. Park large blobs (e.g. a project's gigabytes of
PDFs/STLs) on a home Tailscale node, addressed by content hash under a MagicDNS
name + path. The git repo only ever holds a four-line **pointer** per large file
plus a canonical **`tailvault.lock`**. On checkout the bytes are restored; on
commit they're swapped back out. The storage node is reachable from anywhere on
your tailnet, with no public exposure, no cloud bill, and no API keys.

---

## How it works

Tailvault has two complementary modes:

1. **Repo-managed mode** — large files live *in your git working tree*. Git
   filters transparently swap bytes ⇄ pointers, and `push`/`pull` move the real
   bytes to/from the node. This is the Git-LFS-style workflow.
2. **Vault / federation mode** — files live *only on storage nodes* (no git
   checkout). You operate on them directly (`put`/`get`/`ls`/`mv`/`rm`) across a
   **federation** of multiple nodes that share one logical file tree.

### The moving parts

| Piece | Where it lives | Role |
| --- | --- | --- |
| **Pointer file** | committed in git | Four-line stand-in (`magic`, `sha256`, `size`, `location`) the `clean` filter writes in place of real bytes. |
| **`tailvault.toml`** | committed in git | Project config: which location, what `min_size`, include/exclude globs, history & auto-delete policy. Carries **no** node addresses or credentials. |
| **`tailvault.lock`** | committed in git | Canonical record of *what is stored, where, and when*. Sorted for conflict-free union merges. Each entry can embed the file's full **genesis** identity record, so every clone is an off-node identity backup. |
| **`locations.toml`** | `~/.config/tailvault/`, **never committed** | Maps a location *name* (e.g. `home-pi`) → node address, base path, backend, SSH user. Keeps secrets/addresses out of the repo. |
| **Content store** | on the node, `<base_path>/<subpath>/objects/<sha256>` | Deduplicated, content-addressed blobs. History-on files also keep `refs/<path-id>`. |
| **Catalog** (`meta/catalog.toml`) | on the node | Self-describing vault state: federation roster + one row per tracked file. A materialized projection of the WAL. |
| **WAL** (`meta/wal/`) | on the node | Hash-chained, tamper-evident write-ahead log — the durable, recoverable record of every node op. The catalog is rebuildable from it. |
| **Password** (`meta/auth/passwd`) | on the node | argon2id PHC-string hash gating mutating remote ops. |

### Data flow (repo-managed mode)

```
git add big.pdf      → clean filter   → commits a 4-line pointer (bytes set aside)
git push             → pre-push hook  → preflight node → upload new blobs → update lock
                                        (push FAILS loudly if the node is down)
git pull / checkout  → smudge filter  → fetches blob by sha256 → restores real bytes
git rm big.pdf       → push           → auto-deletes the blob (unless `preserve`)
```

Identity is content-independent: a file's **ID** is `sha256` of its 4-field
*genesis record* (content hash + original path + ingest op id + origin node), so
**moves change the path, never the ID**, and an ID is self-certifying and
recoverable from any clone's lock.

### Federation

Multiple storage nodes can be joined into a **federation** sharing one logical
tree. Reads fan out across reachable members; a file found at a different member
than its recorded home succeeds **with a WARN** (run `tailvault heal`). The
system is **reachability-aware**: it distinguishes "genuinely missing" (exit 5)
from "can't prove absence because a member is offline" (exit 6) — it never
reports a false miss under a partial view. Destructive all-members ops (gc)
**refuse** to run while any member is unreachable.

---

## Features

- **Git-native, not a wrapper** — clean/smudge filters + hooks, no shim binary
  intercepting git.
- **Content-addressed dedup** — identical bytes stored once.
- **History off by default** — anti-bloat; opt in per glob via overrides.
- **Tracked deletes + retention** — auto-delete on git-delete, opt-out with
  `preserve`; tombstones keep a blob in the GC keep-set.
- **Hard-fail preflight** — `tailscale status` → `ping`/`Stat` before any partial
  work; node-down leaves no partial upload and an unadvanced lock.
- **Structured errors** — every failure carries a stable code, a cause, a fix,
  and a bucketed exit code.
- **Multi-node federation** — one logical tree across nodes, reachability-aware
  resolution, roster lifecycle (join/leave/evict).
- **Tamper-evident WAL** — hash-chained per-node log; catalog is rebuildable from
  it (disaster recovery).
- **Self-certifying identity** — recoverable file IDs; every clone is an
  off-node identity backup.
- **Password-gated mutations** — argon2id; reads are never gated (they ride the
  tailnet ACL + SSH).
- **Single static binary** — Go; easy cross-compile to a Pi.

---

## Requirements

- **Go 1.26+** (to build from source).
- **Tailscale** installed and logged in on both the client and the storage node
  (`tailscale` must be in `PATH`; the daemon must be running). Tailvault uses the
  **local, already-authenticated session only** — no Tailscale API login, no
  stored credentials.
- A **storage node** on your tailnet with:
  - **SSH backend** (the supported backend today): SSH access for the configured
    user, and a writable `base_path` (use a real disk such as a USB3 SSD, **not**
    a Pi's boot SD card). *Taildrive backend is planned.*
  - The `tailvault` binary is **not** required on the node for the SSH backend —
    node-side helpers are invoked over SSH.
- **git** (obviously) for repo-managed mode.

---

## Installation

Tailvault builds to a single static binary. The version is embedded at build
time from the [`VERSION`](./VERSION) file — never hardcode it.

### From source (recommended)

```sh
git clone https://github.com/Ibtesam-Mahmood/tailvault.git
cd tailvault

# Build with the embedded version into ./bin/tailvault
make build

# Put it on your PATH
sudo install -m 0755 bin/tailvault /usr/local/bin/tailvault
# …or, for a user-local install:
install -m 0755 bin/tailvault ~/.local/bin/tailvault
```

`make build` runs:

```sh
go build -ldflags "-X github.com/Ibtesam-Mahmood/tailvault/internal/version.Version=$(cat VERSION)" \
  -o bin/tailvault ./cmd/tailvault
```

### With `go install`

```sh
go install github.com/Ibtesam-Mahmood/tailvault/cmd/tailvault@latest
```

> Note: `go install` does not run the Makefile's `-ldflags`, so
> `tailvault --version` will report `dev` instead of the real version. Use
> `make build` if you want the embedded version string.

### Verify

```sh
tailvault --version
tailvault --help
```

---

## Updating

```sh
cd tailvault
git pull
make build
sudo install -m 0755 bin/tailvault /usr/local/bin/tailvault
```

Or, for a `go install`-based setup, re-run `go install …@latest`.

Tailvault's on-node formats are **schema v2** and there is **no v1-migration
machinery** (no real v1 vaults exist) — a reader requires `version = 2` and
rejects anything else with a config error. Updates within the v2 line preserve
on-disk formats; the byte-stable WAL/identity serializations are frozen so a
dependency bump can't silently break an existing chain.

---

## Uninstalling

```sh
# 1. Remove the binary
sudo rm /usr/local/bin/tailvault        # or ~/.local/bin/tailvault

# 2. Remove user-level config & client-side state (optional)
rm -rf ~/.config/tailvault              # locations.toml (node registry)
rm -rf ~/.tailvault                     # pull receipts + federation cache

# 3. Per-repo: stop git from using the filters/hooks
#    Edit .gitattributes to drop the tailvault filter lines, and remove the
#    installed hooks from .git/hooks (pre-push, etc.). Then delete:
#    tailvault.toml, tailvault.lock
```

> Uninstalling the client does **not** touch the bytes on your storage node —
> the content store under `<base_path>/<subpath>/` is left intact. Delete it
> manually on the node if you want the storage reclaimed.

---

## Configuration

### Project config — `tailvault.toml` (committed)

Written by `tailvault init`. Decides what is vault-managed and where, with **no**
node addresses or secrets (those resolve at runtime against `locations.toml`).

```toml
version = 1

[storage]
location = "home-pi"      # name resolved via ~/.config/tailvault/locations.toml
subpath  = "root-pnp"     # optional child folder under the location's base_path

[rules]
min_size    = "5MB"                       # files >= this are vault-managed
include     = ["**/*.pdf", "**/*.stl", "**/*.3mf", "**/*.pptx"]
exclude     = ["**/*.tmp", "drafts/**"]   # exclude WINS over include
history     = false                       # default: no version history (anti-bloat)
auto_delete = true                        # default: prune from storage on git delete

# Per-pattern overrides; FIRST match wins.
[[rules.overrides]]
match    = "masters/**"
history  = true
preserve = true                           # never auto-delete
```

**Rule evaluation (normative):** a file is vault-managed when it does *not* match
any `exclude` glob **and** (it matches an `include` glob **or** its size
`>= min_size`). Size suffixes are **binary** (`MB = 1024²`); `"5MB"` = 5 242 880
bytes. See [`SPEC.md §1, §7`](./SPEC.md).

### Node registry — `locations.toml` (NOT committed)

Lives at `~/.config/tailvault/locations.toml`; carries node addresses and the SSH
login user. Written interactively by `tailvault setup` / `tailvault location add`.

```toml
[locations.home-pi]
node      = "home-pi.tailnet-name.ts.net"  # MagicDNS or 100.x IP
base_path = "/mnt/ssd/tailvault"            # on a USB3 SSD, not the boot SD
backend   = "ssh"                           # ssh | taildrive (taildrive planned)
user      = "ibte"
```

`node` is prefilled from `tailscale status --json` peer enumeration (local
session only). `--node` is always available as a manual fallback.

---

## Usage

### Quick start (repo-managed mode)

```sh
# 1. Register a storage node (interactive — writes ~/.config/tailvault/locations.toml)
tailvault setup

# 2. In your repo: write tailvault.toml + .gitattributes and install hooks
tailvault init

# 3. Track large files by rule or path
tailvault track "**/*.pdf"

# 4. Work normally — bytes are swapped for pointers on commit, restored on checkout
git add . && git commit -m "add big assets"
git push        # preflight + upload blobs + update lock (fails loudly if node down)

# 5. See what's local-only / pushed / drifted / orphaned
tailvault status
```

### Quick start (vault / federation mode)

```sh
tailvault vault init home-pi          # bootstrap a self-describing vault on a location
tailvault vault passwd home-pi        # set the node password (gates mutations)
tailvault fed init home-pi            # create a federation around it
tailvault fed join office-nas         # add another node to the federation

tailvault vault put ./big.stl home-pi/models/big.stl   # ingest (no checkout)
tailvault vault ls                    # browse the federated logical tree
tailvault vault get models/big.stl    # download by path or ID (no password needed)
```

### Command reference

Run `tailvault <command> --help` for full flags. Reads (`ls`/`stat`/`get`/
`status`) are **never** password-gated; mutating remote ops are.

#### Setup & configuration

| Command | What it does |
| --- | --- |
| `tailvault setup` | Interactively register a storage node, then prompts you to run `init`. |
| `tailvault init` | Write `tailvault.toml` + `.gitattributes` and install git hooks in the current repo. |
| `tailvault location add <name>` | Register a tailnode storage target (writes `locations.toml`). |
| `tailvault location list` | List registered locations + live reachability. |

#### Repo-managed workflow

| Command | What it does |
| --- | --- |
| `tailvault track <glob>… \| <location>/<path>` | Add a repo include-rule, or register an existing vault file. |
| `tailvault status` | Show local-only / pushed / drifted / orphaned files. |
| `tailvault push` | Upload diffs, GC deletes, update the lock. (Also runs from the `pre-push` hook.) |
| `tailvault pull` | Fetch the blobs the current tree/branch needs. |
| `tailvault revert <path> <sha>` | Repoint a history-on file to an older stored version. |
| `tailvault heal` | Repoint stale `tailvault.lock` locations from live federation resolution. |

#### Vault operations (checkout-free, on a storage location)

| Command | What it does |
| --- | --- |
| `tailvault vault init` | Bootstrap a location (tracks all files by default; `sync_mode=manual`). |
| `tailvault vault ls [<location>[/<path>]]` | List the federated logical tree (members, or entries under a folder). |
| `tailvault vault stat <path \| id>` | Show one file's metadata and reachability. |
| `tailvault vault get <path \| id>` | Download a federated file by path or ID (no checkout, no password). |
| `tailvault vault put <local-file> <location>/<dest-path>` | Ingest a local file into a location (no checkout). |
| `tailvault vault mv <src> <dest location>/<path>` | Move a file within or between locations (**ID preserved**). |
| `tailvault vault rm <path \| id>` | Delete a file from its location (the only way a manual file dies). |
| `tailvault vault scan <location>` | Reconcile disk against the catalog (absorb manual changes). |
| `tailvault vault sync-mode <path \| id> <git\|manual>` | Change a file's sync mode remotely. |
| `tailvault vault passwd <location>` | Set or change a node's per-node password (**no recovery**). |
| `tailvault vault rebuild-catalog <location>` | Reconstruct a missing/torn catalog from the node's WAL (disaster recovery). |
| `tailvault vault restore-identity <location>/<path>` | Re-seed a rebuilt catalog entry with its original self-certifying ID. |

#### Federation

| Command | What it does |
| --- | --- |
| `tailvault fed init <location>` | Create a federation around an existing vault location. |
| `tailvault fed join <location>` | Join a location to an existing federation. |
| `tailvault fed leave <location>` | Detach a member from its federation (no data deleted). |
| `tailvault fed evict <member>` | Retire a dead member from the federation. |
| `tailvault fed status` | Show the roster, reachability, and last-seen. |

#### Maintenance & recovery

| Command | What it does |
| --- | --- |
| `tailvault gc` | Prune unreferenced blobs per retention policy (`--dry-run` supported). |
| `tailvault verify` | Re-hash stored blobs; report corruption and missing objects. |
| `tailvault ops` | List pending/failed federation WAL ops across reachable members. |
| `tailvault ops retry (<op-id> \| --all)` | Re-run pending/failed ops (client-driven, idempotent). |

#### Internal (invoked by git / over SSH — not for direct use)

`filter-clean`, `filter-smudge`, `__merge-lock`, `node verify-passwd`.

---

## Use cases

- **A leaner alternative to Git LFS** for repos heavy with PDFs, CAD/STL/3MF,
  slide decks, datasets, or media — without the bloat of full version history.
- **Self-hosted large-file storage** on a home Pi + USB3 SSD (or any tailnet
  node), reachable from anywhere on your tailnet with no public exposure and no
  cloud bill.
- **Multi-machine / multi-node setups** where several storage nodes form one
  logical tree (federation), with reachability-aware reads that never silently
  miss a file behind an offline node.
- **Checkout-free asset libraries** — `put`/`get`/`ls` files that live only on
  storage and never need to land in a working tree.
- **Disaster-resilient identity** — every clone's lock is an off-node identity
  backup; a torn catalog can be rebuilt from the node's tamper-evident WAL.

---

## Error codes & exit codes

Failures are **structured**: a stable code, a one-line cause, a concrete fix, and
a **bucketed exit code**. The `pre-push` hook surfaces the same code so a failed
push reads obviously rather than as a generic git error.

| Code | Meaning | Exit |
| --- | --- | --- |
| `TV-CFG-*` | Bad `tailvault.toml`, unknown/missing location, unparseable pointer or lock | 2 |
| `TV-AUTH-01` | Password missing/rejected on a mutating remote op (refused before any work) | 2 |
| `TV-NET-01` | `tailscaled` not reachable / `tailscale` not in PATH | 3 |
| `TV-NET-02` | Not logged into the tailnet (`tailscale up`) | 3 |
| `TV-NODE-01` | Storage node offline/unreachable | 4 |
| `TV-NODE-02` | Node reachable but `base_path` not writable | 4 |
| `TV-OBJ-01` | Expected blob missing on the node (genuinely absent) | 5 |
| `TV-FED-01` | Partial view — not found among reachable members **and** ≥1 member offline (can't prove absence) | 6 |
| `TV-FED-02` | An all-members op (gc) ran with ≥1 member unreachable | 6 |
| `TV-FED-03` | WAL hash-chain verification failed (tamper/corruption) | 6 |
| `TV-FED-04` | ID-collision on `restore-identity` (id already live on a member) | 6 |

Exit buckets: `0` success · `1` unclassified · `2` config/precondition/auth ·
`3` Tailscale down · `4` node unreachable · `5` integrity/missing blob ·
`6` federation/partial-view. See [`SPEC.md §5, §15`](./SPEC.md).

---

## Caveats & known issues

- **Pre-1.0, personal project.** The CLI is implemented and test-covered through
  Blocks 0–4, but there is no tagged stable release; expect rough edges and
  format churn only within the frozen v2 contract.
- **SSH backend only (today).** The **Taildrive** backend is designed but not yet
  the shipped path — use the `ssh` backend.
- **Single active writer assumed (early).** Lock conflicts use a per-path union
  merge driver, but true multi-writer safety needs a create-exclusive backend
  `Put`; concurrent writers from multiple machines aren't fully hardened yet.
- **No password recovery.** The node password (argon2id) has **no recovery
  path** — resetting it requires SSH/physical access to rewrite
  `meta/auth/passwd`. Reads are never gated, so losing the password doesn't lock
  you out of `get`/`ls`/`stat`.
- **No v1 migration.** On-node formats are schema v2; a reader rejects any other
  version with a config error. Old test vaults are recreated, not migrated.
- **Eager smudge.** Checkout fetches all needed blobs eagerly (v1 behavior);
  lazy/partial checkout is a planned later option — large trees pull everything
  the branch references.
- **Destructive ops gate on full reachability.** `gc` refuses while any
  federation member is unreachable (by design — deletes never tolerate a partial
  view); bring all members online to run it.
- **`go install` reports `dev`.** The version string is only embedded via
  `make build`'s `-ldflags`; `go install` builds report `dev`.
- **Tailscale local-session only.** Node discovery uses the local,
  already-authenticated daemon (`tailscale status --json`) — there's no API
  login or stored-credential fallback if the daemon can't enumerate peers (use
  `--node` to enter an address manually).

---

## Status & roadmap

The build runs in phases (0–9), each a block of one or more PRs that close
tracked GitHub issues and keep `main` green. Implementation through **Blocks 0–4**
(core repo workflow + multi-node federation, identity, WAL, auth, recovery) is in
place and test-covered, including a real-CLI end-to-end suite. See
[`tasks/README.md`](./tasks/README.md) for the phase → block map and the critical
path, and [`CHANGELOG.md`](./CHANGELOG.md) for per-version detail.

---

## Documentation

| File | What it is |
| --- | --- |
| [`SPEC.md`](./SPEC.md) | **Normative frozen contract** — the four file schemas, federation formats, error catalogue, resolved open questions. Cite this, don't re-decide. |
| [`proposal.md`](./proposal.md) | Formal proposal: problem, architecture, CLI surface, phased plan, effort estimates. |
| [`DESIGN.md`](./DESIGN.md) | Authoritative design dump: config/lock/pointer schemas, retention model, Tailscale leverage, rejected options. |
| [`CONTRIBUTING.md`](./CONTRIBUTING.md) | Workflow: versioning, task/issue tracking, PR conventions. |
| [`CHANGELOG.md`](./CHANGELOG.md) | Version history (`VERSION` is the single source of truth). |
| [`tasks/`](./tasks/) | Phased implementation backlog (mirrors GitHub issues). |

---

## Contributing

Work is tracked as GitHub issues **and** mirrored as local files in
[`tasks/`](./tasks/). Work happens in **blocks of PRs**: one task ≈ one branch
(`phase-N/<slug>`) ≈ one PR that `Closes #N`, bumps [`VERSION`](./VERSION) by
`+0.0.1`, and adds a matching `## v<version>` entry to
[`CHANGELOG.md`](./CHANGELOG.md) in the **same commit**. `main` stays green.

Build & test:

```sh
make build       # build with embedded version → bin/tailvault
make test        # go test ./...
make vet         # go vet ./...
make fmt         # gofmt -l .
```

See [`CONTRIBUTING.md`](./CONTRIBUTING.md) and [`CLAUDE.md`](./CLAUDE.md) for the
full workflow and locked decisions.
