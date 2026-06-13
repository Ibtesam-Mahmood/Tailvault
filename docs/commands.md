# Usage & command reference

Quick starts for both modes, then the full command surface. Run
`tailvault <command> --help` for full flags. Reads (`ls`/`stat`/`get`/`status`)
are **never** password-gated; mutating remote ops are.

For the concepts behind these commands see [`how-it-works.md`](./how-it-works.md).

---

## Quick start (repo-managed mode)

```sh
# 1. Create a storage location (writes ~/.config/tailvault/locations.toml).
#    Default is a LOCAL store on this machine:
tailvault setup
#    …or register a remote tailnet node instead:
tailvault setup --remote

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

## Quick start (vault / federation mode)

```sh
tailvault vault init node-a          # bootstrap a self-describing vault on a location
tailvault vault passwd node-a        # set the node password (gates mutations)
tailvault fed init node-a            # create a federation around it
tailvault fed join node-b         # add another node to the federation

tailvault vault put ./big.stl node-a/models/big.stl   # ingest (no checkout)
tailvault vault ls                    # browse the federated logical tree
tailvault vault get models/big.stl    # download by path or ID (no password needed)
```

---

## Command reference

### Setup & configuration

| Command | What it does |
| --- | --- |
| `tailvault setup` | **Default: create a local storage location** on this machine (prompts for a name + store path), then prompts you to run `init`. |
| `tailvault setup --remote` | Register a **remote tailnet node** instead (the peer pick-list; `--node` skips it). |
| `tailvault config` | Locate + register the `tailscale` CLI (fixes peer discovery when Tailscale is a GUI app off `PATH`). |
| `tailvault init` | Write `tailvault.toml` + `.gitattributes` and install git hooks in the current repo. |
| `tailvault location add <name>` | Register a storage target (writes `locations.toml`). `--backend local\|ssh\|taildrive`; `local` needs only `--base-path`. |
| `tailvault location ls` | List registered locations + live reachability. |
| `tailvault location rm <name>` | Un-register a location (**double-confirmed**; never touches stored bytes). `--purge` also deletes the local store data (`objects/`,`refs/`,`meta/`) with a **3rd** confirmation. |
| `tailvault location list` | List registered locations + live reachability. |

### Repo-managed workflow

| Command | What it does |
| --- | --- |
| `tailvault track <glob>… \| <location>/<path>` | Add a repo include-rule, or register an existing vault file. |
| `tailvault status` | Show local-only / pushed / drifted / orphaned files. |
| `tailvault push` | Upload diffs, GC deletes, update the lock. (Also runs from the `pre-push` hook.) |
| `tailvault pull` | Fetch the blobs the current tree/branch needs. |
| `tailvault revert <path> <sha>` | Repoint a history-on file to an older stored version. |
| `tailvault heal` | Repoint stale `tailvault.lock` locations from live federation resolution. |

### Vault operations (checkout-free, on a storage location)

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

### Federation

| Command | What it does |
| --- | --- |
| `tailvault fed init <location>` | Create a federation around an existing vault location. |
| `tailvault fed join <location>` | Join a location to an existing federation. |
| `tailvault fed leave <location>` | Detach a member from its federation (no data deleted). |
| `tailvault fed evict <member>` | Retire a dead member from the federation. |
| `tailvault fed status` | Show the roster, reachability, and last-seen. |

### Maintenance & recovery

| Command | What it does |
| --- | --- |
| `tailvault gc` | Prune unreferenced blobs per retention policy (`--dry-run` supported). |
| `tailvault verify` | Re-hash stored blobs; report corruption and missing objects. |
| `tailvault ops` | List pending/failed federation WAL ops across reachable members. |
| `tailvault ops retry (<op-id> \| --all)` | Re-run pending/failed ops (client-driven, idempotent). |
| `tailvault update` | Update / check / uninstall the binary (see [`install.md`](./install.md)). |

### Internal (invoked by git / over SSH — not for direct use)

`filter-clean`, `filter-smudge`, `__merge-lock`, `node verify-passwd`.
