package lock

import toml "github.com/pelletier/go-toml/v2"

// Parse unmarshals a tailvault.lock from raw bytes. It is the byte-oriented
// counterpart to Load: gc reads each branch's lock via `git show
// <branch>:tailvault.lock` and parses that stdout directly, without a file path.
func Parse(data []byte) (*Lock, error) {
	var l Lock
	if err := toml.Unmarshal(data, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// Find returns the entry with the given path and true, or a zero Entry and
// false when absent. Used by revert (to repoint a path) and status (to diff a
// working file against its locked state).
func (l *Lock) Find(path string) (Entry, bool) {
	for _, e := range l.Entries {
		if e.Path == path {
			return e, true
		}
	}
	return Entry{}, false
}

// ReferencedSHAs returns every content sha this lock keeps alive: each entry's
// current SHA256 plus all of its Versions (history-on entries). The result is
// deduplicated and empty shas are skipped; iteration order follows the entry
// slice (current sha before its versions) so the output is deterministic after
// Canonicalize. This is the per-branch keep-set GC unions across branch tips.
func (l *Lock) ReferencedSHAs() []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(sha string) {
		if sha == "" {
			return
		}
		if _, ok := seen[sha]; ok {
			return
		}
		seen[sha] = struct{}{}
		out = append(out, sha)
	}
	for _, e := range l.Entries {
		add(e.SHA256)
		for _, v := range e.Versions {
			add(v)
		}
	}
	return out
}
