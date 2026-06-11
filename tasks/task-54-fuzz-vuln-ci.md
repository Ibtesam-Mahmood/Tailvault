# Task 54: Parser Fuzzing + govulncheck in CI

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Part II → "Security & transport" ("govulncheck CI, parser fuzzing"); "Part II task breakdown" → Block 5 ("fuzz catalog/WAL/genesis parsers") · **Block:** 5 — Security analysis & hardening · **Estimated Effort:** 1.5 ideal eng-days · **Dependencies:** Task 28 (catalog parser), Task 29 (WAL entry parser), Task 30 (genesis records + receipts), Task 33 (`.tailvaultignore` parser), Task 25 (`.github/workflows/ci.yml` exists), Task 51 (threat model — names these parsers as the malicious-client attack surface) · **Type:** Testing

## Summary

The v2 parsers consume bytes written by **other machines** — a malicious or
buggy client (threat-model adversary 3) controls what a node's catalog, WAL,
genesis records, and receipts contain, and a hostile repo controls
`.tailvaultignore`. This task points Go native fuzzing (`go test -fuzz`,
`FuzzXxx` harnesses) at every one of those parse entry points, fixes any
panic/hang/OOM found, commits the seed corpora, and wires both a short fuzz
smoke and `govulncheck` into `.github/workflows/ci.yml` so the protection is
continuous, not one-shot.

The invariant under test is uniform: **hostile bytes must produce a structured
error, never a panic, infinite loop, or unbounded allocation.** A parser that
crashes on garbage breaks the "hard-fail, never silent, always legible" error
contract.

## Context

### Related packages

- `internal/catalog` (28), `internal/wal` (29), `internal/identity` (30 —
  genesis records + receipt files), the `.tailvaultignore` engine (33) —
  **fuzz harnesses added here**, parser fixes applied here.
- `internal/lock` / `internal/pointer` (v1) — already locally-sourced but
  repo-committed (hostile repos exist); add harnesses if absent — cheap wins.
- `.github/workflows/ci.yml` (task 25) — **modified here:** govulncheck job +
  fuzz smoke job.
- `testdata/fuzz/` per package — committed seed corpora.

### Prerequisites

- [ ] Go ≥ 1.18 toolchain in CI (native fuzzing); pin the version used.
- [ ] Each target parser exposes a pure `Parse([]byte) (T, error)`-shaped
      entry point (per the SPEC §8 layering rule, leaf parsers return plain
      errors) — if any parser only reads from disk, refactor a byte-level
      seam first (mechanical, in-scope).

## Changes Required

### Fuzz harnesses (one per parser)

- **Files:** `internal/catalog/fuzz_test.go`, `internal/wal/fuzz_test.go`,
  `internal/identity/fuzz_test.go` (genesis + receipts — two targets),
  `internal/<ignore-pkg>/fuzz_test.go` (`.tailvaultignore`).
- **Action:** create.
- **Purpose:** standard Go fuzz targets, e.g.:

```go
func FuzzParseCatalog(f *testing.F) {
    f.Add(validCatalogBytes)        // seeds: valid, truncated, wrong-version
    f.Fuzz(func(t *testing.T, data []byte) {
        c, err := catalog.Parse(data)
        if err != nil { return }    // structured error = pass
        // round-trip property: what parses must re-serialize and re-parse
        out, err := catalog.Marshal(c)
        if err != nil { t.Fatalf("parsed but won't marshal: %v", err) }
        if _, err := catalog.Parse(out); err != nil {
            t.Fatalf("round-trip re-parse failed: %v", err)
        }
    })
}
```

Per-target properties beyond "no panic":

- **Catalog:** round-trip stability; schema-version mismatch → typed error
  (H7), never a partial struct.
- **WAL entries:** prev-hash field of arbitrary length/content never panics
  the chain verifier (feeds task 53); entry-size limits enforced (reject,
  don't allocate, on absurd declared lengths).
- **Genesis records:** `id == sha256(record)` self-certification (D24) — a
  fuzzed record must either fail verification or be byte-faithful; receipts
  with mismatched ids are rejected.
- **`.tailvaultignore`:** pathological glob patterns (deep `**` nesting, long
  lines, invalid UTF-8) terminate promptly — guard against super-linear
  doublestar matching with a time/size bound in the harness.

### Crash fixes

- **Action:** modify parsers as findings dictate. Typical classes: slice
  bounds on truncated input, `make([]T, declaredLen)` from attacker-declared
  lengths, integer overflow on size fields, unterminated-quote loops. Every
  fix lands with the crasher added to `testdata/fuzz/<Target>/` as a
  regression input.

### Seed corpora

- **Files:** `internal/*/testdata/fuzz/Fuzz*/...`
- **Action:** create + commit. Seeds per target: one valid exemplar (from
  SPEC v2 schemas), one truncated, one wrong-schema-version, one
  maximal-fields, plus every crasher found during this task.

### .github/workflows/ci.yml

- **Action:** modify. Two additions:
  1. **govulncheck job:** `golang.org/x/vuln/cmd/govulncheck@latest` run as
     `govulncheck ./...`; failure fails CI. Pin via `go run` with a version
     once flakiness demands it.
  2. **Fuzz smoke job:** each `Fuzz*` target run with a short budget
     (`-fuzz=<target> -fuzztime=30s`, matrix or loop) on PRs to the affected
     packages; corpora replay (`go test ./...` already replays `testdata/fuzz`
     seeds as unit cases — that is the cheap always-on layer).

## Implementation Checklist

- [ ] Fuzz targets for catalog, WAL entry, genesis record, receipt, and
      `.tailvaultignore` parsers (+ lock/pointer if not already covered).
- [ ] Round-trip / self-certification / termination properties asserted, not
      just no-panic.
- [ ] All crashes found are fixed; each crasher committed as a corpus entry.
- [ ] Seed corpora committed under `testdata/fuzz/`.
- [ ] `govulncheck` job in CI, failing the build on findings.
- [ ] Short-budget fuzz smoke in CI; corpus replay runs in plain `go test`.
- [ ] Parser edge cases discovered appended to `EDGE-CASES.md`.

## Testing Requirements

This task **is** testing; the meta-requirements:

- A locally meaningful fuzz session per target before merging: ≥ 10 minutes
  per target (or until coverage plateaus), documented in the PR description
  with execs/sec and corpus growth.
- `go test ./...` (no `-fuzz`) replays every committed seed + crasher
  deterministically — this is the regression guarantee CI relies on.
- Demonstrate the harness catches bugs: temporarily re-introduce one fixed
  crasher locally and confirm the corpus replay fails (do not commit).
- govulncheck runs clean at merge time (or findings are fixed/upgraded away —
  a known-vulnerable dependency is a finding, not a footnote).

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes (including corpus replay of all crashers).
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
      top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- Every parser named in the threat model's attack-surface table has a fuzz
  target; arbitrary bytes produce structured errors — zero panics/hangs/OOMs
  reachable from fuzzing at merge time.
- Seed corpora (incl. all found crashers) are committed and replayed by plain
  `go test ./...`.
- CI fails on a govulncheck finding and runs the fuzz smoke on PRs.
- Each fixed crash references its corpus file in the commit.

## Related Proposal Sections

> **Security & transport.** … Block 5 is a dedicated security analysis (…
> privacy audit of catalogs/receipts, **govulncheck CI, parser fuzzing**).

> **Part II task breakdown → Block 5.** Threat-model doc; … govulncheck in
> CI; **fuzz catalog/WAL/genesis parsers**; adversarial review of auth paths.

## Notes & Considerations

- **Gotcha:** Go's fuzzer only runs one `-fuzz` target per `go test`
  invocation — the CI smoke needs a loop/matrix, not a single flag.
- **Gotcha:** keep harnesses pure (no disk/network) or execs/sec craters;
  that's why the byte-level `Parse` seam is a prerequisite.
- **Gotcha:** attacker-declared length fields are the classic Go parser OOM —
  bound allocations by input length, never by declared length.
- **For Next Task:** task 55 audits what these same files *reveal* (privacy)
  rather than what they can *break* (memory safety).
- **Prev:** [task-53-wal-chain-verify-tooling](./task-53-wal-chain-verify-tooling.md) ·
  **Next:** [task-55-privacy-audit-ssh-hardening](./task-55-privacy-audit-ssh-hardening.md)
