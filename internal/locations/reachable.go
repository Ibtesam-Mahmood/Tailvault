package locations

import (
	"context"
	"os"
)

// Reachability is the live status of one location, as shown by `location ls`.
type Reachability struct {
	Name      string
	Reachable bool
	Detail    string // e.g. "online", "ping failed"
}

// PingFunc pings a node by MagicDNS name / IP; nil error means reachable.
type PingFunc func(ctx context.Context, node string) error

// Check classifies a location's liveness by pinging its node. The ping func is
// injected so `location ls` is testable with no real tailnet (mirrors the
// Runner seam in internal/tailscale and internal/backend).
//
// Reachability is informational: ls reports it as data, it is never a command
// failure. A nil pinger yields an "unknown" (unreachable=false-positive guard)
// detail without claiming reachability.
func Check(ctx context.Context, name string, loc Location, ping PingFunc) Reachability {
	r := Reachability{Name: name}
	// A local store has no node to ping: reachability is whether its base_path is
	// a usable directory. A not-yet-created path is still "reachable" — the first
	// write creates it (atomicReplace MkdirAll's); only a path that exists as a
	// non-dir or errors on stat (e.g. permission) is unreachable.
	if loc.Backend == BackendLocal {
		fi, err := os.Stat(loc.BasePath)
		switch {
		case err == nil && fi.IsDir():
			r.Reachable, r.Detail = true, "local"
		case os.IsNotExist(err):
			r.Reachable, r.Detail = true, "local (created on first write)"
		case err == nil:
			r.Detail = "local path is not a directory"
		default:
			r.Detail = "local path unavailable"
		}
		return r
	}
	if ping == nil {
		r.Detail = "unknown (no probe)"
		return r
	}
	if err := ping(ctx, loc.Node); err != nil {
		r.Detail = "ping failed"
		return r
	}
	r.Reachable = true
	r.Detail = "online"
	return r
}
