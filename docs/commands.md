# Usage & command reference

Quick starts for both modes, then the full command surface. Run
`example-project <command> --help` for full flags. Reads (`ls`/`stat`/`get`/`status`)
are **never** password-gated; mutating remote ops are.

For the concepts behind these commands see [`how-it-works.md`](./how-it-works.md).

---

## Quick start (repo-managed mode)

```sh
# 1. Register a storage node (interactive — writes ~/.config/example-project/locations.toml)
example-project setup

# 2. In your repo: write example-project.toml + .gitattributes and install hooks
example-project init

# 3. Track large files by rule or path
example-project track "**/*.pdf"

# 4. Work normally — bytes are swapped for pointers on commit, restored on checkout
git add . && git commit -m "add big assets"
git push        # preflight + upload blobs + update lock (fails loudly if node down)

# 5. See what's local-only / pushed / drifted / orphaned
example-project status
```

## Quick start (vault / federation mode)

```sh
example-project vault init node-a          # bootstrap a self-describing vault on a location
example-project vault passwd node-a        # set the node password (gates mutations)
example-project fed init node-a            # create a federation around it
example-project fed join node-b         # add another node to the federation

example-project vault put ./big.stl node-a/models/big.stl   # ingest (no checkout)
example-project vault ls                    # browse the federated logical tree
example-project vault get models/big.stl    # download by path or ID (no password needed)
```

---

## Command reference

### Setup & configuration

| Command | What it does |
| --- | --- |
| `example-project setup` | Interactively register a storage node, then prompts you to run `init`. |
| `example-project init` | Write `example-project.toml` + `.gitattributes` and install git hooks in the current repo. |
| `example-project location add <name>` | Register a tailnode storage target (writes `locations.toml`). |
| `example-project location list` | List registered locations + live reachability. |

### Repo-managed workflow

| Command | What it does |
| --- | --- |
| `example-project track <glob>… \| <location>/<path>` | Add a repo include-rule, or register an existing vault file. |
| `example-project status` | Show local-only / pushed / drifted / orphaned files. |
| `example-project push` | Upload diffs, GC deletes, update the lock. (Also runs from the `pre-push` hook.) |
| `example-project pull` | Fetch the blobs the current tree/branch needs. |
| `example-project revert <path> <sha>` | Repoint a history-on file to an older stored version. |
| `example-project heal` | Repoint stale `example-project.lock` locations from live federation resolution. |

### Vault operations (checkout-free, on a storage location)

| Command | What it does |
| --- | --- |
| `example-project vault init` | Bootstrap a location (tracks all files by default; `sync_mode=manual`). |
| `example-project vault ls [<location>[/<path>]]` | List the federated logical tree (members, or entries under a folder). |
| `example-project vault stat <path \| id>` | Show one file's metadata and reachability. |
| `example-project vault get <path \| id>` | Download a federated file by path or ID (no checkout, no password). |
| `example-project vault put <local-file> <location>/<dest-path>` | Ingest a local file into a location (no checkout). |
| `example-project vault mv <src> <dest location>/<path>` | Move a file within or between locations (**ID preserved**). |
| `example-project vault rm <path \| id>` | Delete a file from its location (the only way a manual file dies). |
| `example-project vault scan <location>` | Reconcile disk against the catalog (absorb manual changes). |
| `example-project vault sync-mode <path \| id> <git\|manual>` | Change a file's sync mode remotely. |
| `example-project vault passwd <location>` | Set or change a node's per-node password (**no recovery**). |
| `example-project vault rebuild-catalog <location>` | Reconstruct a missing/torn catalog from the node's WAL (disaster recovery). |
| `example-project vault restore-identity <location>/<path>` | Re-seed a rebuilt catalog entry with its original self-certifying ID. |

### Federation

| Command | What it does |
| --- | --- |
| `example-project fed init <location>` | Create a federation around an existing vault location. |
| `example-project fed join <location>` | Join a location to an existing federation. |
| `example-project fed leave <location>` | Detach a member from its federation (no data deleted). |
| `example-project fed evict <member>` | Retire a dead member from the federation. |
| `example-project fed status` | Show the roster, reachability, and last-seen. |

### Maintenance & recovery

| Command | What it does |
| --- | --- |
| `example-project gc` | Prune unreferenced blobs per retention policy (`--dry-run` supported). |
| `example-project verify` | Re-hash stored blobs; report corruption and missing objects. |
| `example-project ops` | List pending/failed federation WAL ops across reachable members. |
| `example-project ops retry (<op-id> \| --all)` | Re-run pending/failed ops (client-driven, idempotent). |
| `example-project update` | Update / check / uninstall the binary (see [`install.md`](./install.md)). |

### Internal (invoked by git / over SSH — not for direct use)

`filter-clean`, `filter-smudge`, `__merge-lock`, `node verify-passwd`.
