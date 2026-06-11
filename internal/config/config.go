// Package config owns the repo-committed project config tailvault.toml:
// parse, validate, and stable round-trip write via pelletier/go-toml/v2.
package config

import (
	"fmt"
	"os"

	toml "github.com/pelletier/go-toml/v2"
)

// Config mirrors the proposal's tailvault.toml block exactly.
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

// Override is a per-pattern rule; the slice order is significant
// (first-match-wins, resolved by internal/rules).
type Override struct {
	Match    string `toml:"match"`
	History  bool   `toml:"history"`
	Preserve bool   `toml:"preserve"`
}

// Default returns a Config seeded with the locked spec defaults (version 1,
// min_size "5MB", auto_delete true, history false). It is used both as the
// pre-unmarshal seed in Load — so a TOML that omits a key keeps the spec
// default rather than Go's zero value — and by `init` (Task 18) to write a
// fresh tailvault.toml. The caller must set [storage].location before the
// config will pass Validate.
func Default() Config {
	return Config{
		Version: 1,
		Rules:   Rules{MinSize: "5MB", AutoDelete: true},
	}
}

// Load reads, defaults, unmarshals, and validates a tailvault.toml.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate enforces version == 1, a non-empty location, and a parseable
// min_size. Violations are config/precondition errors (exit bucket 2).
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

// Write marshals the config as stable, diff-friendly TOML (go-toml/v2 emits
// fields in declaration order).
func Write(path string, c *Config) error {
	b, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
