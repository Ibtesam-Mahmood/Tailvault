package config

import (
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// ValidateGlob returns an error if pattern is not a usable doublestar glob.
func ValidateGlob(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("empty glob")
	}
	if !doublestar.ValidatePattern(pattern) {
		return fmt.Errorf("invalid glob %q", pattern)
	}
	return nil
}

// AddInclude appends pattern to c.Rules.Include if absent, preserving existing
// order (append-only). Returns added=false when the pattern was already present
// (idempotent), so the caller can avoid a needless write.
func (c *Config) AddInclude(pattern string) (added bool) {
	for _, g := range c.Rules.Include {
		if g == pattern {
			return false
		}
	}
	c.Rules.Include = append(c.Rules.Include, pattern)
	return true
}
