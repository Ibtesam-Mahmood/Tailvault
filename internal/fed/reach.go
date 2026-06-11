package fed

import (
	"context"
	"sort"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
)

// Reach records, for one operation, which of the members its scope touches
// actually answered. It is the substrate for per-operation reachability scoping
// (D27): there is no global "federation online" — each command needs only the
// members its scope references, and this value reports who of those answered.
// Reach is attached to every downstream remote view (resolution Results,
// vault ls/stat output) so the user always sees the reachability basis of a
// result. It is a plain serializable value and decides nothing on its own:
// mapping reachability to a TV-FED vs TV-OBJ verdict belongs to the resolution
// engine (Task 32), not here. All three slices are sorted for determinism.
type Reach struct {
	Required    []string // member names the op's scope touches (D27)
	Answered    []string // members that responded to the probe
	Unreachable []string // members that errored or did not answer in time
}

// Probe pings each required member through the injected prober concurrently and
// returns the accounting. The prober is injected (production passes a
// tailscale.Ping / backend Stat seam; tests pass a stub) so this package never
// makes a real network call. Probe honors ctx: if ctx is cancelled or its
// deadline passes, members that have not yet answered are recorded as
// Unreachable rather than blocking the caller — for a hard per-member bound,
// pass a ctx with a deadline or an injected prober that self-times-out.
func Probe(ctx context.Context, members []catalog.Member, probe func(ctx context.Context, m catalog.Member) error) Reach {
	r := Reach{Required: make([]string, 0, len(members))}
	for _, m := range members {
		r.Required = append(r.Required, m.Name)
	}
	sort.Strings(r.Required)

	type result struct {
		name string
		ok   bool
	}
	// Buffered so a slow/hung probe goroutine can always send and exit without
	// blocking after we have stopped collecting (ctx cancellation) — no leak
	// that wedges the process.
	ch := make(chan result, len(members))
	for _, m := range members {
		go func(m catalog.Member) {
			err := probe(ctx, m)
			ch <- result{name: m.Name, ok: err == nil}
		}(m)
	}

	answered := make(map[string]bool, len(members))
collect:
	for range members {
		select {
		case <-ctx.Done():
			break collect
		case res := <-ch:
			answered[res.name] = res.ok
		}
	}

	for _, m := range members {
		if answered[m.Name] {
			r.Answered = append(r.Answered, m.Name)
		} else {
			// Either the probe failed or ctx ended before it answered: both mean
			// "we could not confirm this member" — bias to Unreachable.
			r.Unreachable = append(r.Unreachable, m.Name)
		}
	}
	sort.Strings(r.Answered)
	sort.Strings(r.Unreachable)
	return r
}

// AllAnswered reports that every required member answered — the precondition for
// all-members operations (the gc gate, Task 36).
func (r Reach) AllAnswered() bool { return len(r.Unreachable) == 0 }

// Partial reports that at least one required member was unreachable — the
// condition that turns a not-found into TV-FED-01 ("cannot prove absence")
// rather than TV-OBJ-01.
func (r Reach) Partial() bool { return len(r.Unreachable) > 0 }
