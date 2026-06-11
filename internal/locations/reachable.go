package locations

import (
	"context"
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
