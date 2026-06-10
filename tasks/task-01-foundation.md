# Task 01 — Foundation

**Phase:** 1 · **Label:** `Task`, `phase-1` · **Est:** 2 d · **GitHub:** #_TBD_

## Goal

A compiling Go CLI skeleton with config/lock parsing and the rule engine.

## Acceptance criteria

- [ ] `go.mod` initialised; Cobra root command with `init`, `status`,
      `location add` stubs; `go build ./...` succeeds.
- [ ] `internal/config` parses & validates `vault.toml` (round-trip safe).
- [ ] `internal/lock` parses & writes `vault.lock` in canonical form.
- [ ] `internal/rules` implements `min_size` + include/exclude glob matching
      (doublestar `**`), with override precedence.
- [ ] `VERSION` embedded at build time; `tailvault --version` prints it.
- [ ] Unit tests for config/lock round-trip and rule matching.

## Suggested layout

`cmd/tailvault/main.go`, `internal/config/`, `internal/lock/`, `internal/rules/`.

## Likely deps

`spf13/cobra`, `pelletier/go-toml/v2`, `bmatcuk/doublestar/v4`.

## References

- `proposal.md` — Phase 1; project structure & deps.
- Task 00 frozen schemas.
