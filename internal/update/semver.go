// Package update implements tailvault's self-update story: querying the latest
// GitHub release, comparing it to the running build, a cached passive "update
// available" check that long-lived commands surface, and the download → verify
// → atomic-replace path behind `tailvault update`.
//
// Everything that touches the network or the filesystem is reached through a
// small seam (Fetcher / the Client fields) so the logic is exercised in tests
// with fakes — no real GitHub calls, consistent with the repo's test discipline.
package update

import (
	"fmt"
	"strconv"
	"strings"
)

// semver is tailvault's MAJOR.MINOR.PATCH triple. Tailvault versions are simple
// (no pre-release/build metadata in the contract), so a 3-int compare suffices.
type semver struct{ major, minor, patch int }

// parseSemver accepts "v0.0.106", "0.0.106", or a leading-v tag and returns the
// triple. The "dev" placeholder and anything unparseable return ok=false so
// callers treat the running build as "unknown" and never claim a stale update.
func parseSemver(s string) (semver, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" || s == "dev" {
		return semver{}, false
	}
	// Drop any pre-release/build suffix defensively (e.g. "1.2.3-rc1").
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		out[i] = n
	}
	return semver{out[0], out[1], out[2]}, true
}

func (v semver) String() string { return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch) }

// less reports whether v < o.
func (v semver) less(o semver) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	if v.minor != o.minor {
		return v.minor < o.minor
	}
	return v.patch < o.patch
}

// NewerAvailable reports whether latest is a strictly higher version than
// current. It is conservative: if either side is unparseable (e.g. current is a
// "dev" build, or a malformed tag came back), it returns false — we never nag
// about an "update" we cannot prove is newer.
func NewerAvailable(current, latest string) bool {
	cv, ok1 := parseSemver(current)
	lv, ok2 := parseSemver(latest)
	if !ok1 || !ok2 {
		return false
	}
	return cv.less(lv)
}
