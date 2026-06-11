# How Tailvault works

The concepts behind Tailvault: its two modes, the moving parts, data flow,
identity, and federation. For the day-to-day commands see
[`commands.md`](./commands.md); for config files see
[`configuration.md`](./configuration.md).

---

## The idea in one breath

Keep the laptop clone lean. Park large blobs (e.g. a project's gigabytes of
PDFs/STLs) on a home Tailscale node, addressed by content hash under a MagicDNS
name + path. The git repo only ever holds a four-line **pointer** per large file
plus a canonical **`tailvault.lock`**. On checkout the bytes are restored; on
commit they're swapped back out. The storage node is reachable from anywhere on
your tailnet, with no public exposure, no cloud bill, and no API keys.

---

## Two modes

1. **Repo-managed mode** — large files live *in your git working tree*. Git
   filters transparently swap bytes ⇄ pointers, and `push`/`pull` move the real
   bytes to/from the node. This is the Git-LFS-style workflow.
2. **Vault / federation mode** — files live *only on storage nodes* (no git
   checkout). You operate on them directly (`put`/`get`/`ls`/`mv`/`rm`) across a
   **federation** of multiple nodes that share one logical file tree.

---

## The moving parts

| Piece | Where it lives | Role |
| --- | --- | --- |
| **Pointer file** | committed in git | Four-line stand-in (`magic`, `sha256`, `size`, `location`) the `clean` filter writes in place of real bytes. |
| **`tailvault.toml`** | committed in git | Project config: which location, what `min_size`, include/exclude globs, history & auto-delete policy. Carries **no** node addresses or credentials. |
| **`tailvault.lock`** | committed in git | Canonical record of *what is stored, where, and when*. Sorted for conflict-free union merges. Each entry can embed the file's full **genesis** identity record, so every clone is an off-node identity backup. |
| **`locations.toml`** | `~/.config/tailvault/`, **never committed** | Maps a location *name* (e.g. `node-a`) → node address, base path, backend, SSH user. Keeps secrets/addresses out of the repo. |
| **Content store** | on the node, `<base_path>/<subpath>/objects/<sha256>` | Deduplicated, content-addressed blobs. History-on files also keep `refs/<path-id>`. |
| **Catalog** (`meta/catalog.toml`) | on the node | Self-describing vault state: federation roster + one row per tracked file. A materialized projection of the WAL. |
| **WAL** (`meta/wal/`) | on the node | Hash-chained, tamper-evident write-ahead log — the durable, recoverable record of every node op. The catalog is rebuildable from it. |
| **Password** (`meta/auth/passwd`) | on the node | argon2id PHC-string hash gating mutating remote ops. |

---

## Data flow (repo-managed mode)

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

---

## Federation

Multiple storage nodes can be joined into a **federation** sharing one logical
tree. Reads fan out across reachable members; a file found at a different member
than its recorded home succeeds **with a WARN** (run `tailvault heal`). The system
is **reachability-aware**: it distinguishes "genuinely missing" (exit 5) from
"can't prove absence because a member is offline" (exit 6) — it never reports a
false miss under a partial view. Destructive all-members ops (gc) **refuse** to
run while any member is unreachable.

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
- **Structured errors** — every failure carries a stable code, a cause, a fix, and
  a bucketed exit code (see [`troubleshooting.md`](./troubleshooting.md)).
- **Multi-node federation** — one logical tree across nodes, reachability-aware
  resolution, roster lifecycle (join/leave/evict).
- **Tamper-evident WAL** — hash-chained per-node log; catalog is rebuildable from
  it (disaster recovery).
- **Self-certifying identity** — recoverable file IDs; every clone is an off-node
  identity backup.
- **Password-gated mutations** — argon2id; reads are never gated (they ride the
  tailnet ACL + SSH).
- **Single static binary** — Go; easy cross-compile to a Pi.

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
