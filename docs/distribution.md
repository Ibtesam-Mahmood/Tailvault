# Tailvault Distribution Plan (private repo)

How Tailvault is built, released, installed, updated, and uninstalled **while the
GitHub repo is private**. The architecture is identical to the public story
([`public-release.md`](./public-release.md)) — going public is a permissions
flip, not a re-platform.

> Status: infrastructure implemented in v0.0.106. This doc is the operating
> manual; the public-release doc is the forward plan.

---

## The backbone: tag → GoReleaser → GitHub Release

A single source of truth for distribution: **a pushed git tag triggers
GitHub Actions → GoReleaser**, which cross-compiles, embeds the version, signs +
checksums the archives, publishes a GitHub Release, and updates the Homebrew tap.
Every install channel consumes those Release assets.

```
git tag v0.0.106 && git push origin v0.0.106
   └─> .github/workflows/release.yml   (on: push tags v*)
         └─> goreleaser release --clean   (.goreleaser.yaml)
               ├─ before hook: assert tag == VERSION file  ← never ship a mislabeled binary
               ├─ build  darwin/linux/windows × amd64/arm64   (ldflags embed version)
               ├─ archive  tailvault_<version>_<os>_<arch>.tar.gz (+ .zip on windows)
               ├─ checksums.txt (sha256) + SBOM
               ├─ cosign keyless signature (GitHub OIDC)
               ├─ push Homebrew formula → Ibtesam-Mahmood/homebrew-tap (private)
               └─ publish the GitHub Release + assets
```

The archive name in `.goreleaser.yaml` (`archives.name_template`) is a **contract**
with the self-updater (`internal/update.ArchiveName`) and `install.sh`. Changing
one means changing all three.

### Components in this repo

| Path | Role |
|---|---|
| `.goreleaser.yaml` | build/archive/checksum/sign/brew config; tag↔VERSION guard |
| `.github/workflows/release.yml` | tag-triggered release job (needs `contents: write`, `id-token: write`) |
| `install.sh` / `uninstall.sh` | token/gh-aware shell install for non-Homebrew users |
| `internal/version` | build-time ldflags + `go install` build-info fallback |
| `internal/update` | `tailvault update` (`--check`/apply/`--uninstall`) + passive notice |

---

## Install channels (all fed by the same Release)

| Channel | User command | Private-repo requirement |
|---|---|---|
| Homebrew | `brew install Ibtesam-Mahmood/tap/tailvault` | `gh auth` / `HOMEBREW_GITHUB_API_TOKEN` |
| Shell installer | `curl -fsSL .../install.sh \| sh` | `gh` logged in, or `GITHUB_TOKEN` set |
| `go install` | `go install github.com/Ibtesam-Mahmood/tailvault/cmd/tailvault@latest` | `GOPRIVATE` + git `insteadOf` |
| Self-update | `tailvault update` | `GITHUB_TOKEN` (or `GH_TOKEN`) |
| Raw binary | download from the Release page | logged-in browser / gh |

All channels resolve to the same versioned assets, so **install == update**.

### `go install` one-time setup (private)

```sh
go env -w GOPRIVATE=github.com/Ibtesam-Mahmood/*
git config --global url."git@github.com:".insteadOf "https://github.com/"
go install github.com/Ibtesam-Mahmood/tailvault/cmd/tailvault@latest
```

`go install` ignores `-ldflags`, but `internal/version` reads the module build
info as a fallback, so `tailvault --version` still reports the real tag.

---

## In-CLI update / uninstall (`tailvault update`)

```
tailvault update              # download latest, verify checksum, replace binary in place
tailvault update --check      # report whether a newer release exists (no changes)
tailvault update --version vX.Y.Z   # install a specific release (pin / downgrade)
tailvault update --uninstall  # remove the binary + client-side state (confirms first)
tailvault update -y …         # non-interactive (CI)
```

- **Verification is mandatory**: the downloaded archive's SHA-256 must match the
  Release `checksums.txt` or the update aborts and the existing binary is left
  untouched. (cosign signatures are published too, for out-of-band verification.)
- **Passive notice**: `status` and `pull` append a one-line "update available"
  hint. It is cached in `~/.tailvault/update-check.json` (~once/day), bounded by
  a 3s timeout, silent on any failure, and disabled by `TAILVAULT_NO_UPDATE_CHECK=1`.
- **Windows**: in-place self-update is unsupported (the OS locks the running
  exe); Windows users reinstall via the installer or `go install`.
- **Uninstall scope**: removes the binary, `~/.config/tailvault` (node registry),
  and `~/.tailvault` (receipts + fed cache). It never deletes storage-node bytes
  or per-repo `tailvault.toml`/`tailvault.lock`.

---

## Manual steps — one-time setup (maintainer)

These are the things **I cannot do for you**; they need your GitHub account/permissions.

### A. Create the private Homebrew tap repo
1. Create a new **private** repo named exactly **`homebrew-tap`** under your
   account: `https://github.com/Ibtesam-Mahmood/homebrew-tap`.
2. Initialize it with a README (GoReleaser will create `Formula/tailvault.rb`).

### B. Create a tap-write token and add it as a secret
1. GitHub → Settings → Developer settings → **Fine-grained personal access
   token**. Repository access: **only `homebrew-tap`**. Permission:
   **Contents: Read and write**.
2. In the **tailvault** repo → Settings → Secrets and variables → Actions →
   **New repository secret**:
   - Name: `HOMEBREW_TAP_TOKEN`
   - Value: the token from step 1.
   (`GITHUB_TOKEN` for the Release itself is provided automatically by Actions.)

### C. Confirm Actions permissions
- tailvault repo → Settings → Actions → General → Workflow permissions →
  **Read and write permissions** (the workflow also self-declares
  `contents: write` + `id-token: write`, which is what cosign needs).

### D. Cut the first release
```sh
# VERSION is already bumped per task; just tag it.
git tag v0.0.106
git push origin v0.0.106
# Watch: Actions tab → "release" workflow.
```
To rehearse locally first (no publish): `goreleaser release --snapshot --clean`
(needs `goreleaser` and `cosign` installed locally).

### E. Tell users how to authenticate (private phase)
Until the repo is public, share with each user **one** of:
- `gh auth login` (then Homebrew + installer + `tailvault update` all work), or
- a read-only fine-grained token they export as `GITHUB_TOKEN`.

---

## Per-release checklist (maintainer)

1. Land the task PR (bumps `VERSION` + `CHANGELOG.md` in the same commit — the
   existing hard rule).
2. `git checkout main && git pull`.
3. `git tag v$(cat VERSION) && git push origin v$(cat VERSION)`.
4. Watch the `release` workflow go green.
5. `brew upgrade tailvault` (or `tailvault update --check`) to smoke-test.

The GoReleaser `before` hook fails the release if the tag and `VERSION` disagree,
so a mismatched tag can never publish.

---

## Known constraints (private phase)

- Homebrew/installer/self-update all need a token or `gh` session — unavoidable
  for a private repo. All three degrade gracefully to a clear "set a token"
  message rather than a cryptic 404.
- `browser_download_url` 404s on private repos without a session; the self-updater
  and installer therefore download via the API asset URL with the token.
- No anonymous `curl | sh` yet — that arrives the moment the repo goes public
  (see [`public-release.md`](./public-release.md)).
