package lock

// Merge performs a per-path union 3-way merge of two committed locks, used by
// the custom git merge driver (Task 24) so two branches that both pushed merge
// without garbling the TOML. base is informational (add-vs-modify) but the union
// rule does not need it; a nil base is fine.
//
// Rules, keyed by entry path:
//   - a path on only one side is kept as-is (disjoint edits union cleanly);
//   - a path on both sides with the same sha256 is kept once, unioning versions[];
//     the entry is Deleted only if BOTH sides are tombstones — **live beats
//     tombstone**: if any branch still has the file live, the merged lock
//     presents it live so pull materializes the file that genuinely exists on a
//     branch (preferring the tombstone there would silently drop a live file);
//   - a path on both sides with differing sha256 resolves deterministically:
//     newest pushed_at wins, tie broken by the lexicographically greater sha256,
//     and versions[] are unioned (winner-first) so no historical blob reference
//     is lost (keeps GC's keep-set and revert correct after a merge).
//
// The result is built into a fresh Lock and Canonicalize()d, so the output is
// path-sorted and byte-identical regardless of input ordering — re-serializing
// through the canonical writer is what stops ordering-only conflicts on the next
// merge.
func Merge(base, ours, theirs *Lock) (*Lock, error) {
	byPath := make(map[string]Entry)
	for _, e := range entriesOf(ours) {
		byPath[e.Path] = e
	}
	for _, e := range entriesOf(theirs) {
		o, both := byPath[e.Path]
		switch {
		case !both:
			byPath[e.Path] = e // disjoint -> union
		case o.SHA256 == e.SHA256:
			o.Versions = unionVersions(o.Versions, e.Versions) // identical sha -> keep, merge history
			o.Deleted = o.Deleted && e.Deleted                 // live beats tombstone: deleted only if BOTH are
			byPath[e.Path] = o
		default:
			byPath[e.Path] = resolve(o, e) // differing sha -> deterministic winner
		}
	}
	merged := &Lock{Entries: make([]Entry, 0, len(byPath))}
	for _, e := range byPath {
		merged.Entries = append(merged.Entries, e)
	}
	merged.Canonicalize()
	return merged, nil
}

// resolve picks the winning entry for a differing-sha conflict: newest pushed_at
// wins; an exact tie is broken by the lexicographically greater sha256 for total
// determinism (never wall-clock or map order). versions[] are unioned winner-first.
func resolve(a, b Entry) Entry {
	winner, loser := a, b
	switch {
	case b.PushedAt.After(a.PushedAt):
		winner, loser = b, a
	case a.PushedAt.After(b.PushedAt):
		winner, loser = a, b
	default: // equal pushed_at -> greater sha wins
		if b.SHA256 > a.SHA256 {
			winner, loser = b, a
		}
	}
	winner.Versions = unionVersions(winner.Versions, loser.Versions)
	return winner
}

// unionVersions concatenates primary then secondary, dropping empties and
// duplicates while preserving first-seen order (primary, i.e. newest-first, is
// kept ahead of secondary). Returns nil when empty so history-off entries emit
// no versions key.
func unionVersions(primary, secondary []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, list := range [][]string{primary, secondary} {
		for _, s := range list {
			if s == "" {
				continue
			}
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

// entriesOf returns l.Entries or nil for a nil lock.
func entriesOf(l *Lock) []Entry {
	if l == nil {
		return nil
	}
	return l.Entries
}
