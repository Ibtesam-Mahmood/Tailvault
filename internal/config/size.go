package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Size suffixes are interpreted as BINARY units (powers of 1024), so "5MB"
// resolves to 5 * 1024 * 1024 = 5_242_880 bytes. This is the frozen MB-vs-MiB
// binding (see SPEC.md §7): a single rule for all suffixes, matching the
// proposal's intuition that "5 MB" is the on-disk threshold. IEC spellings
// (KiB/MiB/…) are accepted as explicit synonyms for the same binary values.
var unitFactors = map[string]int64{
	"B":  1,
	"KB": 1 << 10, "KIB": 1 << 10,
	"MB": 1 << 20, "MIB": 1 << 20,
	"GB": 1 << 30, "GIB": 1 << 30,
	"TB": 1 << 40, "TIB": 1 << 40,
}

// suffixOrder lists suffixes longest-first so e.g. "MIB" is matched before "MB"
// and "B" never shadows a longer unit.
var suffixOrder = []string{"KIB", "MIB", "GIB", "TIB", "KB", "MB", "GB", "TB", "B"}

// ParseSize parses a human size string ("5MB", "512KB", "1.5GiB", "1048576")
// into a byte count using binary units. A bare number is bytes. Matching is
// case-insensitive and an optional single space between number and suffix is
// allowed. The result is truncated to a whole number of bytes.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	for _, u := range suffixOrder {
		if strings.HasSuffix(s, u) {
			num := strings.TrimSpace(strings.TrimSuffix(s, u))
			if num == "" {
				return 0, fmt.Errorf("missing number before unit %q", u)
			}
			v, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0, err
			}
			if v < 0 {
				return 0, fmt.Errorf("negative size %q", s)
			}
			return int64(v * float64(unitFactors[u])), nil
		}
	}
	// bare number → bytes
	return strconv.ParseInt(s, 10, 64)
}
