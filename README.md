# Tailvault

`tailvault` is a CLI that keeps large binary files **out of git history** while
keeping them in sync with `git push` / `git pull`. The real bytes live in a
content-addressed folder on a **Tailscale node** (e.g. a home Raspberry Pi with a
USB3 SSD); the git repo carries only small **pointer files** and a **lock file**.
A clean, bloat-averse alternative to Git LFS — history is **off by default**,
deletes are tracked, and a green `git push` guarantees the bytes actually landed
on storage or fails loudly.

> **Version 0.0.1 — bootstrap.** The design is frozen; there is no runnable
> implementation yet. The build is being executed in phased PR blocks. See
> [`proposal.md`](./proposal.md) and [`DESIGN.md`](./DESIGN.md).

## The idea in one breath

Keep the laptop clone lean. Park large blobs (e.g. `root-pnp`'s ~1.1 GB of
PDFs/STLs) on a home Tailscale node, addressed by MagicDNS name + path. History
is **off by default** (single current ref per file, no version chain) to prevent
bloat, but diffs and deletes are still tracked; versioning is opt-in per file.
Files deleted from git are auto-deleted from storage unless marked `preserve`.
Hard-fails if the node is down or an expected blob is missing — never a silent
success.

## Documentation

| File | What it is |
| --- | --- |
| [`proposal.md`](./proposal.md) | Formal proposal: problem, architecture, CLI surface, phased plan, effort estimates. |
| [`DESIGN.md`](./DESIGN.md) | Authoritative design dump: config/lock/pointer schemas, retention model, Tailscale leverage, rejected options, open questions. |
| [`CONTRIBUTING.md`](./CONTRIBUTING.md) | Workflow: versioning, task/issue tracking, PR conventions. |
| [`CHANGELOG.md`](./CHANGELOG.md) | Version history. |
| [`tasks/`](./tasks/) | Phased implementation backlog (mirrors GitHub issues). |

## Status & roadmap

The build runs in 10 phases (0–9), from spec freeze to dogfooding on `root-pnp`.
Each phase is a block of one or more PRs that close tracked GitHub issues. See
[`tasks/README.md`](./tasks/README.md) for the phase → block map and
[`CONTRIBUTING.md`](./CONTRIBUTING.md) for how work is organized.
