# Task 06: Pointer File Format

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Detailed Design → "Pointer file (the in-git stand-in)"; Testing Strategy → Unit ("Hashing + pointer round-trip"); Open Questions → Q6 · **Block:** 1 — MVP · **Estimated Effort:** 0.5 ideal eng-day · **Dependencies:** Task 02 (Go module + Cobra CLI skeleton — gives the module path, `internal/` layout, and VERSION embedding) · **Type:** Foundation

## Summary

The pointer file is the tiny text stand-in that git stores in place of a large
file's real bytes. The `clean` filter replaces a managed file's content with a
pointer on commit; the `smudge` filter restores the real bytes on checkout
(Task 17). This task owns the exact serialization: encode a `Pointer` to bytes,
decode bytes back to a `Pointer`, and cheaply sniff whether an arbitrary blob of
bytes *is* a pointer (so the filter can pass non-pointer content through
untouched).

The format is fixed and minimal — four newline-terminated lines with a stable
header so future versions can be detected. `internal/pointer` is a pure,
dependency-light package (no Tailscale, no I/O beyond byte slices) that the
filter, `status`, `push`, and `pull` all build on. Getting the bytes exactly
right here means every downstream consumer can trust a single canonical
representation.

This is a Foundation task: small, self-contained, and heavily unit-tested. It
must round-trip losslessly and reject malformed input rather than silently
producing a half-populated `Pointer`.

## Context

### Related packages

- `internal/pointer` — **created here.** Encode/Decode + `IsPointer` sniff.
- `internal/filter` (Task 17) — calls `Encode` in `clean`, `Decode`/`IsPointer`
  in `smudge`.
- `internal/lock` (Task 04) — shares the `sha256`/`size`/`location` triple
  conceptually; pointer is the per-file in-tree echo of a lock entry.

### Prerequisites

- [ ] Task 02 merged: Go module exists, module path known, `internal/` present.
- [ ] Confirm the exact pointer schema from the proposal (four lines, below).

## Changes Required

### internal/pointer/pointer.go

- **File:** `internal/pointer/pointer.go`
- **Action:** create
- **Purpose:** define the `Pointer` type and the encode/decode/sniff API.

Exact on-disk format (the proposal's "Pointer file" block), four lines, each
`\n`-terminated:

```
tailvault.v1
sha256 9f2b1c…
size 41231873
location home-pi
```

```go
package pointer

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// Magic is the required first line of every pointer file.
const Magic = "tailvault.v1"

// Pointer is the in-git stand-in for a vault-managed file's bytes.
type Pointer struct {
	SHA256   string // lowercase hex sha256 of the real content
	Size     int64  // size in bytes of the real content
	Location string // location name resolved via locations.toml
}

// Encode renders p as the canonical 4-line pointer file (trailing newline).
func Encode(p Pointer) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s\n", Magic)
	fmt.Fprintf(&b, "sha256 %s\n", p.SHA256)
	fmt.Fprintf(&b, "size %d\n", p.Size)
	fmt.Fprintf(&b, "location %s\n", p.Location)
	return b.Bytes()
}

// IsPointer is a cheap prefix check: does data begin with the pointer magic?
// Used by the smudge/clean filter to pass non-pointer content through untouched
// without a full parse.
func IsPointer(data []byte) bool {
	return bytes.HasPrefix(data, []byte(Magic+"\n")) ||
		bytes.Equal(bytes.TrimRight(data, "\n"), []byte(Magic))
}

// Decode parses a pointer file, rejecting a wrong header or any malformed line.
func Decode(data []byte) (Pointer, error) {
	// scan lines; require exactly the 4 keys in order; validate sha hex + size int
	// return a typed parse error on the first violation
}
```

Implementation Notes:

- `Decode` must reject: a first line that is not exactly `tailvault.v1`; a
  missing/extra/reordered key line; a non-`key value` shape; a `size` that is
  not a base-10 non-negative int; an empty `sha256` or `location`.
- Trim a single trailing `\n` per line via `bufio.Scanner`; tolerate the file
  having or lacking a final trailing newline but nothing else.
- Keep `sha256` opaque-but-validated: require it to be non-empty lowercase hex
  (length check is fine — full 64-char enforcement is a reasonable strictness
  choice; document whichever you pick).

Key Considerations:

- `IsPointer` must be allocation-cheap and never panic on arbitrary binary
  input (it will be fed real PDFs/STLs by the filter).
- Errors returned by `Decode` should be plain `error` values here (the
  `tserr` typed-error layer wraps at the command boundary, not in this leaf
  package) — but make them descriptive (`pointer: line 1: want %q, got %q`).

## Implementation Checklist

- [ ] `Pointer` struct with `SHA256`, `Size int64`, `Location`.
- [ ] `Magic` const = `tailvault.v1`.
- [ ] `Encode` produces exactly four `\n`-terminated lines.
- [ ] `Decode` enforces header + line order + value validity.
- [ ] `IsPointer` cheap prefix sniff, panic-free on binary input.
- [ ] Descriptive parse errors with line context.

## Testing Requirements

`internal/pointer/pointer_test.go` — table-driven:

- **Round-trip:** `Decode(Encode(p)) == p` for several pointers (zero size,
  large size, typical sha).
- **Proposal sample:** the literal four-line block from the proposal parses to
  the expected struct.
- **Reject bad header:** first line `tailvault.v2`, `foobar`, or empty → error.
- **Reject malformed lines:** missing `size`, reordered keys, `size abc`,
  negative size, blank `sha256`, extra trailing line → error.
- **`IsPointer`:** true for an encoded pointer and for bare-magic input; false
  for random bytes, empty input, and a JPEG-like header (`\xFF\xD8\xFF`).

Fixtures: inline byte-slice literals; no external files needed.

## Validation Checklist

- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes (new pointer tests included).
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` reports nothing.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to the
  top of `CHANGELOG.md` **in the same commit**.

## Acceptance Criteria

- A `Pointer` encoded and re-decoded is byte-for-byte and field-for-field
  identical.
- The proposal's exact four-line sample decodes without error.
- Every malformed/wrong-header case returns a non-nil error and a zero-value
  `Pointer`.
- `IsPointer` correctly classifies pointer vs non-pointer bytes with no panics.

## Related Proposal Sections

> ```
> tailvault.v1
> sha256 9f2b1c…
> size 41231873
> location home-pi
> ```
> The `clean` filter replaces a large file's bytes with this on commit; `smudge`
> restores real bytes on checkout.

> **Testing Strategy → Unit:** Hashing + pointer round-trip (`clean` → pointer →
> `smudge` → bytes).

> **Q6 — Pointer resolution on checkout.** … The pointer carries size+location
> [so the filter can] fetch lazily / batch.

## Notes & Considerations

- **Gotcha:** do not let `Encode`/`Decode` drift apart — the round-trip test is
  the contract. Any future field (e.g. `versions`) belongs in the lock, not the
  pointer.
- **Gotcha:** `IsPointer` is on the hot path of every staged file; keep it a
  prefix compare, not a full `Decode`.
- **For Next Task:** Task 17 (clean/smudge filter) consumes `Encode`,
  `Decode`, and `IsPointer`; keep the API stable.
- **Prev:** [task-05-rule-engine](./task-05-rule-engine.md) ·
  **Next:** [task-07-error-model](./task-07-error-model.md)
