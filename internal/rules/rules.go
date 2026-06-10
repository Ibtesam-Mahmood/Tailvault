// Package rules is the policy core: given a slash-normalized, repo-relative
// path and a size, it decides whether a file is vault-managed and resolves its
// effective history/preserve flags. track, status, push, the clean/smudge
// filter, and GC all consult this so the decision is consistent everywhere.
package rules

import (
	"github.com/bmatcuk/doublestar/v4"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
)

// Decision is the outcome of evaluating a path against the rules.
type Decision struct {
	Managed  bool
	History  bool // effective, after overrides
	Preserve bool // effective, after overrides
}

// Evaluate decides whether path (size bytes) is vault-managed and resolves the
// effective history/preserve flags. The caller MUST pass a slash-normalized,
// repo-relative path (use filepath.ToSlash) so globs behave identically across
// OSes. The function is pure: it performs no disk access.
//
// A file is managed when (size >= min_size OR it matches an include glob) AND
// it matches no exclude glob (exclude always wins). For a managed file, the
// first matching override (in declaration order) sets history/preserve;
// otherwise the global [rules].history and preserve=false apply. Overrides only
// tune flags — they never make an unmanaged file managed.
func Evaluate(cfg *config.Config, path string, size int64) (Decision, error) {
	minSize, err := config.ParseSize(cfg.Rules.MinSize)
	if err != nil {
		return Decision{}, err
	}

	included, err := matchAny(cfg.Rules.Include, path)
	if err != nil {
		return Decision{}, err
	}
	excluded, err := matchAny(cfg.Rules.Exclude, path)
	if err != nil {
		return Decision{}, err
	}

	managed := (size >= minSize || included) && !excluded

	d := Decision{
		Managed:  managed,
		History:  cfg.Rules.History, // global default
		Preserve: false,             // global default
	}
	if !managed {
		return d, nil
	}

	// First-match-wins override resolution (managed files only).
	for _, o := range cfg.Rules.Overrides {
		ok, err := doublestar.Match(o.Match, path)
		if err != nil {
			return Decision{}, err
		}
		if ok {
			d.History = o.History
			d.Preserve = o.Preserve
			break
		}
	}
	return d, nil
}

// matchAny reports whether path matches any of the globs. A malformed glob
// returns an error rather than silently not matching.
func matchAny(globs []string, path string) (bool, error) {
	for _, g := range globs {
		ok, err := doublestar.Match(g, path)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
