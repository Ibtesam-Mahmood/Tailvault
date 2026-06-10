# Task 03: Config parse/validate/write — `tailvault.toml`

**Proposal:** [proposal.md](../proposal.md) · **Proposal Section:** Detailed Design → `tailvault.toml`; rules; Open Question Q5 (default `min_size` 5 MB) · **Block:** 1 (MVP) · **Estimated Effort:** 0.5 day · **Dependencies:** Task 02 (Go module + CLI skeleton) · **Type:** Foundation

## Summary

`internal/config` owns the repo-committed project config `tailvault.toml`: parsing, validating, and writing it back as a stable round-trip using `pelletier/go-toml/v2`. The structs mirror the proposal's config block exactly — `version`, `[storage]` (`location`, `subpath`), `[rules]` (`min_size`, `include`, `exclude`, `history`, `auto_delete`), and the ordered `[[rules.overrides]]` list (`match`, `history`, `preserve`).

The one piece of real logic here is parsing the human `min_size` string (`"5MB"`) into a byte count. Per Open Question Q5 the default is **5 MB**; this task must decide and document the MB-vs-MiB binding and back-fill that decision into `SPEC.md` (Task 01 left it as a TODO). Validation enforces `version == 1` and a non-empty `[storage].location`, returning a config-bucket error (exit 2, per the error model) for violations.

End-state: `config.Load(path)` returns a validated struct; `config.Write(path, cfg)` produces stable, diff-friendly TOML; the proposal's sample parses cleanly and the overrides survive a round-trip in order. This feeds the rule engine (Task 05) and the `init`/`track` commands.

## Context

### Related packages
- `internal/config` — this task.
- Consumed by: `internal/rules` (Task 05), `internal/cli` `init`/`track` (Tasks 18/12).
- Depends on: `github.com/pelletier/go-toml/v2`.

### Prerequisites
- [ ] Task 02 — module exists, `go.mod` present, CLI skeleton compiles.
- [ ] Task 01 — `SPEC.md` defines the field/default/validation table.

## Changes Required

**File:** `go.mod`
**Action:** Add dependency `github.com/pelletier/go-toml/v2`.

**File:** `internal/config/config.go`
**Action:** Create.
**Purpose:** Structs + Load/Write/Validate.

```go
package config

import (
	"fmt"
	"os"

	toml "github.com/pelletier/go-toml/v2"
)

type Config struct {
	Version int     `toml:"version"`
	Storage Storage `toml:"storage"`
	Rules   Rules   `toml:"rules"`
}

type Storage struct {
	Location string `toml:"location"`
	Subpath  string `toml:"subpath,omitempty"`
}

type Rules struct {
	MinSize    string     `toml:"min_size"`
	Include    []string   `toml:"include"`
	Exclude    []string   `toml:"exclude"`
	History    bool       `toml:"history"`
	AutoDelete bool       `toml:"auto_delete"`
	Overrides  []Override `toml:"overrides"`
}

type Override struct {
	Match    string `toml:"match"`
	History  bool   `toml:"history"`
	Preserve bool   `toml:"preserve"`
}

// Defaults applied before unmarshal so unspecified fields match the spec.
func defaults() Config {
	return Config{
		Version: 1,
		Rules:   Rules{MinSize: "5MB", AutoDelete: true},
	}
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := defaults()
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version %d (want 1)", c.Version)
	}
	if c.Storage.Location == "" {
		return fmt.Errorf("[storage].location is required")
	}
	if _, err := ParseSize(c.Rules.MinSize); err != nil {
		return fmt.Errorf("invalid min_size %q: %w", c.Rules.MinSize, err)
	}
	return nil
}

func Write(path string, c *Config) error {
	b, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
```

**File:** `internal/config/size.go`
**Action:** Create.
**Purpose:** Parse human size strings to bytes. **Decision: binary units** — `MB`/`KB`/`GB` are interpreted as powers of 1024 so `"5MB"` → `5 * 1024 * 1024 = 5_242_880`. (Document this binding in `SPEC.md`; it matches the proposal's intuition that "5 MB" is the on-disk threshold and keeps a single rule for all suffixes.)

```go
package config

import (
	"fmt"
	"strconv"
	"strings"
)

var unitFactors = map[string]int64{
	"B": 1, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40,
}

// ParseSize parses "5MB", "512KB", "1024", etc. into bytes (binary units).
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	for _, u := range []string{"TB", "GB", "MB", "KB", "B"} {
		if strings.HasSuffix(s, u) {
			num := strings.TrimSpace(strings.TrimSuffix(s, u))
			v, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0, err
			}
			return int64(v * float64(unitFactors[u])), nil
		}
	}
	// bare number → bytes
	return strconv.ParseInt(s, 10, 64)
}
```

**Implementation Notes:**
- Apply defaults **before** unmarshal so a TOML omitting `auto_delete` keeps `true` (the spec default) rather than Go's zero `false`. (Note: go-toml does not reset unspecified fields, so seeding the struct works.)
- Keep `Override` a slice to preserve order — first-match-wins (Task 05) depends on it.
- `Write` should be deterministic; go-toml/v2 marshals struct fields in declaration order, which gives stable diffs.

**Key Considerations:**
- `auto_delete` default `true` is the subtle one: a user who writes `[rules]` without it must still get auto-delete on. The defaults-before-unmarshal pattern covers this.
- Validation errors here map to **exit bucket 2** (config/precondition) once Task 07's error model exists — wrap them then.

## Implementation Checklist
- [ ] Add `pelletier/go-toml/v2` to `go.mod`.
- [ ] `Config`/`Storage`/`Rules`/`Override` structs mirroring the proposal block.
- [ ] `defaults()` seeding `version=1`, `min_size="5MB"`, `auto_delete=true`.
- [ ] `Load` (read → defaults → unmarshal → validate).
- [ ] `Validate` (version==1, non-empty location, parseable min_size).
- [ ] `Write` (stable marshal).
- [ ] `ParseSize` (binary units) + documented MB-vs-MiB binding back-filled into `SPEC.md`.

## Testing Requirements

`internal/config/config_test.go` + `size_test.go`, table-driven. Use the proposal's sample as a fixture (`testdata/sample.toml`).

| Case | Input | Expect |
|---|---|---|
| parse proposal sample | the proposal's `tailvault.toml` block | location `home-pi`, subpath `root-pnp`, include has 4 globs, override `masters/**` history+preserve true |
| `"5MB"` → bytes | `ParseSize("5MB")` | `5*1024*1024` = 5242880 |
| `"512KB"` | `ParseSize("512KB")` | 524288 |
| bare number | `ParseSize("1048576")` | 1048576 |
| invalid version | `version = 2` | `Load` errors |
| missing location | no `[storage].location` | `Validate` errors |
| overrides order | two overrides | slice order preserved after round-trip |
| round-trip stable | Load → Write → Load | structs deep-equal |

Fixtures: a valid sample, a `version=2` file, a location-less file.

## Validation Checklist
- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` passes.
- [ ] `go vet ./...` clean.
- [ ] `gofmt -l .` clean.
- [ ] Bump `VERSION` by `+0.0.1` and add a matching `## v<version>` entry to `CHANGELOG.md` in the same commit.

## Acceptance Criteria
- The proposal's sample `tailvault.toml` round-trips without loss, overrides in order.
- `ParseSize("5MB") == 5242880` and the MB-vs-MiB choice is documented in `SPEC.md`.
- `version != 1` and empty `location` both produce validation errors.
- Stable round-trip: Load → Write → Load yields identical structs.

## Related Proposal Sections
- **Detailed Design → `tailvault.toml`** — the exact `[storage]` / `[rules]` / `[[rules.overrides]]` block this mirrors.
- **Open Question Q5** — "Default `min_size`… **Recommend: 5 MB**, overridable per project."

## Notes & Considerations
- **Gotcha:** TOML unmarshal will not overwrite a defaulted `true` with a missing key — good — but it *will* overwrite with an explicit `auto_delete = false`. Test both.
- **Gotcha:** float parse for `"1.5MB"` is allowed by `ParseSize`; truncation to int64 is intentional (sub-byte sizes are meaningless).
- **For Next Task:** Task 05's rule engine consumes `Config` + a parsed `min_size` (bytes) and the ordered `Overrides`. Expose `ParseSize` as exported so the engine reuses it rather than re-parsing.
- Prev: [task-02-go-module-cli-skeleton.md](./task-02-go-module-cli-skeleton.md) · Next: [task-04-lock-parse.md](./task-04-lock-parse.md)
