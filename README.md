# Example Project

**A Tailscale-native, content-addressed large-file store that syncs with
`git push` / `git pull`.**

`example-project` keeps large binary files **out of git history** while keeping them in
lockstep with your normal git workflow. The real bytes live in a content-addressed
folder on a **Tailscale node** (e.g. a home Raspberry Pi with a USB3 SSD); the git
repo carries only small **pointer files** and a committed **lock file**. It is a
clean, bloat-averse alternative to Git LFS — **not a wrapper** — built on
git-native filters and hooks.

The headline guarantees:

- **History is off by default** (one current ref per file) to prevent repo bloat —
  but diffs and deletes are still tracked, and history is opt-in per file.
- **Deletes propagate.** A file removed from git is auto-deleted from storage
  unless you mark it `preserve`.
- **No silent success.** Every command that needs the node runs a preflight and
  **hard-fails loudly** if the node is down or an expected blob is missing — a
  green `git push` means the bytes actually landed on storage.

> **Status — v0.0.110, under active phased development.** The CLI is implemented
> and test-covered through Blocks 0–4 (core workflow + multi-node federation), and
> tagged releases ship prebuilt, checksummed binaries via Homebrew, a shell
> installer, `go install`, and `example-project update`. Design is frozen in
> [`SPEC.md`](./SPEC.md). Personal project, pre-1.0 — installable, no stability
> commitment yet.

---

## Install

**Homebrew is the recommended path** — pick **one** channel; they all resolve to
the same signed, checksummed GitHub Release, so install and update are the same
action. **This repo is private**, so installs need GitHub auth (`gh auth login`,
or a read-only token).

```sh
export HOMEBREW_GITHUB_API_TOKEN=$(gh auth token)   # private-repo auth; omit when public
brew install example-org/tap/example-project
example-project --version
```

Update with `brew upgrade example-project`; uninstall with `brew uninstall example-project`.

<details>
<summary><b>Prefer not to use Homebrew?</b> You only need one of these instead.</summary>

```sh
# shell installer (servers / CI / no Homebrew)
GITHUB_TOKEN=$(gh auth token) sh -c "$(curl -fsSL https://raw.githubusercontent.com/example-org/example-project/main/install.sh)"

# go install (Go devs)
go env -w GOPRIVATE=github.com/example-org/*
go install github.com/example-org/example-project/cmd/example-project@latest
```

Then `example-project update` self-updates; `example-project update --uninstall` removes it.
</details>

📖 **Full install, update, uninstall, requirements, and how to share with others
(grant access + auth) → [`docs/install.md`](./docs/install.md).**

---

## Quick start (repo-managed mode)

```sh
example-project setup                  # register a storage node (interactive)
example-project init                   # write example-project.toml + .gitattributes + hooks
example-project track "**/*.pdf"       # track large files by rule or path
git add . && git commit -m "add big assets"
git push                         # preflight + upload blobs + update lock
example-project status                 # local-only / pushed / drifted / orphaned
```

📖 **Vault/federation quick start and the full command reference →
[`docs/commands.md`](./docs/commands.md).**

---

## Documentation

| Doc | What it is |
| --- | --- |
| [`docs/how-it-works.md`](./docs/how-it-works.md) | The two modes, moving parts, data flow, identity, federation, features, use cases. |
| [`docs/install.md`](./docs/install.md) | Install / update / uninstall / **share**, all channels, requirements. |
| [`docs/commands.md`](./docs/commands.md) | Quick starts + full command reference. |
| [`docs/configuration.md`](./docs/configuration.md) | `example-project.toml` + `locations.toml` + state dirs. |
| [`docs/troubleshooting.md`](./docs/troubleshooting.md) | Error/exit codes + caveats & known issues. |
| [`docs/distribution.md`](./docs/distribution.md) | Release pipeline + maintainer setup (private). |
| [`docs/public-release.md`](./docs/public-release.md) | The go-public runbook. |
| [`SPEC.md`](./SPEC.md) | **Normative frozen contract** — file schemas, federation formats, error catalogue. |
| [`DESIGN.md`](./DESIGN.md) | Authoritative design dump: schemas, retention model, rejected options. |
| [`proposal.md`](./proposal.md) | Formal proposal: problem, architecture, phased plan. |
| [`CONTRIBUTING.md`](./CONTRIBUTING.md) | Workflow: versioning, task/issue tracking, PR conventions. |
| [`CHANGELOG.md`](./CHANGELOG.md) | Version history (`VERSION` is the single source of truth). |
| [`tasks/`](./tasks/) | Phased implementation backlog (mirrors GitHub issues). |

---

## Requirements (in brief)

**Tailscale** (logged in, daemon running, local session only) on the client and a
**storage node** with the **SSH backend** + a writable `base_path` on a real disk;
**git** for repo-managed mode; **Go 1.26+** only to build from source. Full detail
in [`docs/install.md`](./docs/install.md#requirements).

---

## Contributing

Work is tracked as GitHub issues **and** mirrored in [`tasks/`](./tasks/), in
**blocks of PRs**: one task ≈ one branch (`phase-N/<slug>`) ≈ one PR that
`Closes #N`, bumps [`VERSION`](./VERSION) by `+0.0.1`, and adds a matching
`## v<version>` entry to [`CHANGELOG.md`](./CHANGELOG.md) in the same commit.
`main` stays green.

```sh
make build       # build with embedded version → bin/example-project
make test        # go test ./...
make vet         # go vet ./...
make fmt         # gofmt -l .
```

See [`CONTRIBUTING.md`](./CONTRIBUTING.md) and [`CLAUDE.md`](./CLAUDE.md) for the
full workflow and locked decisions.
