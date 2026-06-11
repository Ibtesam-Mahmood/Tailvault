// Package ingest is the walk → filter → hash → WAL → catalog engine shared by
// `vault init` (bootstrap, Task 33), `vault scan` (Task 34) and `track` manual
// mode (Block 4). It tracks everything by default (opt-out posture, D18 path 2):
// the only opt-outs are a .tailvaultignore at the vault root and an interactive
// deselect, and an explicit track always beats an ignore. Plain errors only
// (§8 layering); the command boundary maps to tserr.
package ingest

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Ignore is a parsed .tailvaultignore: gitignore-style doublestar globs, one per
// line; '#' comments and blank lines skipped; later lines win; '!' re-includes
// (gitignore negation).
type Ignore struct {
	patterns []ignorePattern
}

type ignorePattern struct {
	glob    string // normalized doublestar glob, matched against the vault-relative path
	negated bool   // '!' prefix: re-includes
}

// IgnoreFileName is the vault-root opt-out file (D22).
const IgnoreFileName = ".tailvaultignore"

// LoadIgnore reads root/.tailvaultignore. A missing file yields an empty Ignore
// (track everything).
func LoadIgnore(root string) (*Ignore, error) {
	b, err := os.ReadFile(filepath.Join(root, IgnoreFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return &Ignore{}, nil
		}
		return nil, err
	}
	return ParseIgnore(b)
}

// ParseIgnore compiles ignore patterns. A syntactically invalid glob errors,
// naming the offending line.
func ParseIgnore(b []byte) (*Ignore, error) {
	ig := &Ignore{}
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := false
		if strings.HasPrefix(line, "!") {
			negated = true
			line = strings.TrimSpace(line[1:])
			if line == "" {
				continue
			}
		}
		// A leading slash anchors to the root; we match vault-relative paths
		// without a leading slash, so strip it.
		line = strings.TrimPrefix(line, "/")
		for _, g := range expandGlob(line) {
			if !doublestar.ValidatePattern(g) {
				return nil, &BadPatternError{Pattern: raw}
			}
			ig.patterns = append(ig.patterns, ignorePattern{glob: g, negated: negated})
		}
	}
	return ig, nil
}

// BadPatternError reports an invalid ignore glob, naming the source line.
type BadPatternError struct{ Pattern string }

func (e *BadPatternError) Error() string {
	return "ingest: invalid .tailvaultignore pattern: " + e.Pattern
}

// expandGlob turns one gitignore-style line into the doublestar glob(s) that
// match a vault-relative path. A trailing '/' means "this dir and everything
// under it"; a pattern with no '/' matches at any depth (gitignore basename
// semantics).
func expandGlob(line string) []string {
	if strings.HasSuffix(line, "/") {
		dir := strings.TrimSuffix(line, "/")
		return []string{dir, dir + "/**"}
	}
	if !strings.Contains(line, "/") {
		// Match the basename at any depth, and the file itself at root.
		return []string{line, "**/" + line}
	}
	return []string{line}
}

// Match reports whether rel (slash-separated, vault-relative) is ignored. The
// last matching pattern wins (gitignore semantics). An explicitly-tracked path
// always wins over any ignore (D22).
func (ig *Ignore) Match(rel string, explicit map[string]bool) bool {
	if explicit[rel] {
		return false
	}
	ignored := false
	for _, p := range ig.patterns {
		if ok, _ := doublestar.Match(p.glob, rel); ok {
			ignored = !p.negated
		}
	}
	return ignored
}
