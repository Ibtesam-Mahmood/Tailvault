# Tailvault — Usage Guide

Tailvault keeps large binary files **out of git history** while staying in lockstep
with `git push` / `git pull`. The real bytes live in a content-addressed folder on a
Tailscale node; your repo carries only small pointer files and a `tailvault.lock`. A
green `git push` means the bytes actually landed — or it fails loudly.

This guide gets you from install to day-to-day use to rollback without reading the
source.

---

## Install

Tailvault is a single static Go binary.

```sh
# Build from source (Go 1.26+):
go build -o tailvault ./cmd/tailvault
# then put it on your PATH, e.g.
install tailvault /usr/local/bin/tailvault
```

Release binaries are published per-OS/arch (darwin/linux, amd64/arm64). `tailvault
--version` prints the embedded version.

**Prerequisites**

- Tailscale installed and logged into your tailnet on both this machine and the
  storage node (`tailscale status` should list the node).
- A storage node reachable over the tailnet with a writable folder — for ~1 GB+
  vaults, a USB3 SSD, not a boot SD card.

---

## Initialize a repo

Two ways to make a plain git repo tailvault-ready:

### `tailvault setup` (interactive)

Picks a storage node from your live tailnet (read from the local `tailscale status
--json` session — no Tailscale login or API token is ever requested or stored),
prompts for the base path and backend, writes `tailvault.toml`, registers the
filter, and installs the hooks.

### `tailvault init` (non-interactive)

```sh
tailvault init                 # writes a default tailvault.toml (empty location)
tailvault init --location home-pi   # prefills storage.location
```

`init` is **idempotent** — re-running it never duplicates `.gitattributes` lines or
clobbers an existing `tailvault.toml`. It:

1. writes a default `tailvault.toml` (`min_size = "5MB"`, `history = false`,
   `auto_delete = true`) if one does not already exist;
2. registers the clean/smudge filter and the lock merge driver in `.gitattributes`
   and git config;
3. installs the `pre-push`, `post-merge`, and `post-checkout` hooks.

Register the storage node (user-level, **not** committed):

```sh
tailvault location add home-pi          # pick from the tailnet interactively
tailvault location add home-pi --node home-pi.tailnet-name.ts.net   # or set manually
tailvault location ls                   # list locations + live reachability
```

This writes `~/.config/tailvault/locations.toml`, which holds node addresses and the
SSH user and is **never** committed.

### Files that land in your repo

| File | Purpose | Committed? |
|---|---|---|
| `tailvault.toml` | project config: location, size threshold, globs, flags | yes |
| `tailvault.lock` | path → current sha, size, location, pusher, history | yes |
| `.gitattributes` | registers `filter=tailvault` + `merge=tailvault` | yes |
| `~/.config/tailvault/locations.toml` | node addresses + SSH user | **no** |

---

## Track files

`tailvault.toml`'s `[rules]` decide what is vault-managed. A file is managed when it
matches an `include` glob **or** is `>= min_size`, and matches no `exclude` glob
(exclude always wins).

```sh
tailvault track '**/*.pdf'     # add an include glob
tailvault track '**/*.stl'
```

```toml
[rules]
min_size    = "5MB"            # binary units: 5MB = 5*1024*1024 = 5_242_880 bytes
                               # (MiB is an accepted synonym for the same value)
include     = ["**/*.pdf", "**/*.stl"]
exclude     = ["**/*.tmp", "drafts/**"]
history     = false            # default: no version history (anti-bloat)
auto_delete = true             # prune from storage when deleted from git

# Per-pattern overrides; first match wins.
[[rules.overrides]]
match    = "masters/**"
history  = true                # keep prior versions for these
preserve = true                # never auto-delete these
```

- **`history`** (off by default): when on, each content change keeps the prior sha so
  you can `revert`. Off keeps only the current blob (and GC reclaims superseded ones).
- **`preserve`**: a still-tracked preserve file's blob is never swept by `gc`.

---

## Day-to-day

The hooks make blobs ride along with git:

- **`git push`** → the `pre-push` hook runs `tailvault push`: it uploads changed
  blobs (dedup-on-`Stat`, so moves/renames and re-pushes transfer nothing), updates
  `tailvault.lock`, and **only then** lets git advance the refs. If the node is down
  or a blob fails to land, the push aborts with a legible error — refs never get
  ahead of storage.
- **`git pull` / `git checkout`** → the `post-merge` / `post-checkout` hooks run
  `tailvault pull`, fetching (and integrity-checking) the blobs the new tree needs.

Manual commands:

```sh
tailvault status        # local-only / pushed / drifted / orphaned
tailvault push          # upload diffs + update the lock
tailvault pull          # fetch blobs the current tree needs
tailvault gc --dry-run  # list blobs that would be pruned
tailvault gc            # prune unreferenced blobs (per-branch keep-set)
```

`gc` builds its keep-set from the **union of every local branch's committed
`tailvault.lock`**, so deleting a file on one branch never prunes a blob another
branch still references.

---

## Recovery & rollback

- **`tailvault verify`** — re-hashes every stored blob (a digest that no longer
  equals its content-addressed key is **corruption**) and confirms every lock-
  referenced sha (current + history `versions`) still exists on the node (a missing
  one is **`TV-OBJ-01`**). Read-only; exit 5 if anything is wrong.
- **`tailvault revert <path> <sha>`** — for a **history-on** file, repoints the lock's
  current sha to a recorded prior `<sha>`, re-materializes the working file from the
  blob, and stages the lock. (History-off files have no prior versions by design.)
- **Full rollback to plain git** — tailvault is purely additive. Remove the
  `filter.tailvault.*` / `merge.tailvault.*` git config, delete the `filter=tailvault`
  and `merge=tailvault` lines from `.gitattributes`, remove the three hooks from
  `.git/hooks`, and restore the real files (`tailvault pull` first if needed). The
  pointers and lock are just files; deleting them returns the repo to ordinary git.

---

## Error codes

Every command that needs the node preflights it and aborts before doing partial work.
Exit codes are bucketed so scripts and the `pre-push` hook can branch on the cause:

| Exit | Meaning |
|---|---|
| `0` | success |
| `2` | config / precondition (bad `tailvault.toml`, unknown location, bad pointer/lock) — `TV-CFG-01` |
| `3` | network / Tailscale down — `TV-NET-01/02` |
| `4` | node unreachable — `TV-NODE-01/02` |
| `5` | integrity / missing blob — `TV-OBJ-01` |

---

## Known limitations (v1)

These are accepted v1 deviations, tracked as follow-ups:

- **Taildrive: mount detection.** The Taildrive backend operates on a locally mounted
  share path. The **caller must ensure the share is actually mounted** — an existing
  but *unmounted* mountpoint is **not** detected, and writes may silently land on the
  local filesystem instead of the node. The **SSH backend is the hardened MVP**;
  Taildrive is the later, lighter-touch option. Prefer SSH for the ~1 GB vault until
  Taildrive mount-state detection lands.
- **`verify` over SSH is O(n) round-trips on hashing.** `tailvault verify` currently
  streams each blob over SSH and hashes it locally; on a large vault this is a known
  cost (minutes for ~1 GB). A future optimization will short-circuit with a remote
  `sha256sum` over the tailnet so only the digest crosses the wire. Until then, run
  `verify` sparingly on large SSH vaults (the Taildrive path hashes locally and is
  unaffected).

---

## Running the tests

```sh
go test ./...                      # fast unit tests
go test -tags integration ./...    # + end-to-end scenarios (SSH tests self-skip
                                   #   when passwordless `ssh localhost` is unavailable)
```
