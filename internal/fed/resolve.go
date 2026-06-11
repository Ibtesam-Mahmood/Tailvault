package fed

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
)

// maxMoveHops bounds how far the resolver follows a chain of moved_to
// forwarding pointers before giving up — a guard against a corrupt or
// adversarial chain. Cycles are caught earlier by the visited set.
const maxMoveHops = 4

// MemberView is what one member reports for a resolution query — the answer
// derived from reading its catalog (and its pending WAL state) over the
// backend. The member executes nothing; this is a pure read (serverless).
type MemberView struct {
	Member      string       // the member that produced this view
	File        catalog.File // the matched file; zero unless Found
	Found       bool         // the member currently holds the id
	MovedTo     string       // forwarding pointer: destination member from a moved_to record
	PendingMove bool         // a pending (in-flight) move intent on this id exists here
}

// Outcome classifies a resolution per SPEC v2 §15. There are exactly four
// classes; no fifth may be invented.
type Outcome int

const (
	// FoundAtHome — the file is at its recorded home member. Success.
	FoundAtHome Outcome = iota
	// FoundElsewhere — found at a different reachable member (fan-out or a
	// moved_to pointer). Success + WARN ("run `tailvault heal`").
	FoundElsewhere
	// PartialView — not found among reachable members with ≥1 unreachable, or an
	// in-flight move, or a forwarding pointer to an unreachable destination.
	// "Cannot prove absence." The command boundary maps this to
	// tserr.FedPartialViewErr (exit 6).
	PartialView
	// Missing — not found with ALL members reachable and no pending move. The
	// command boundary maps this to tserr.ObjMissingErr (exit 5).
	Missing
)

func (o Outcome) String() string {
	switch o {
	case FoundAtHome:
		return "found-at-home"
	case FoundElsewhere:
		return "found-elsewhere"
	case PartialView:
		return "partial-view"
	case Missing:
		return "missing"
	default:
		return fmt.Sprintf("Outcome(%d)", int(o))
	}
}

// Result is the full resolution answer. Reach is ALWAYS populated so every
// remote view carries its reachability basis. For a PartialView whose cause is
// an unreachable forwarding destination, View.MovedTo names that destination
// (even though View.Found is false) so the boundary can print "moved to X
// (offline)".
type Result struct {
	Outcome  Outcome
	View     MemberView     // the winning view for Found* outcomes
	Home     string         // recorded home hint, if any
	Reach    Reach          // who, of the members the scope touched, answered
	LastSeen *MemberSummary // advisory cache color (may be nil); never changes Outcome
}

// Querier fetches one member's view of an id — its catalog answer plus whether a
// pending move intent on the id exists in its WAL. It is injected so production
// reads catalogs/WALs over the backend while tests use a stub; the resolver
// itself never makes a network call.
type Querier interface {
	Query(ctx context.Context, m catalog.Member, id string) (MemberView, error)
}

// Resolver answers "where does this file live right now, and what may I conclude
// from who answered?" by fanning out over the roster's active members.
type Resolver struct {
	Roster Roster
	Q      Querier
	Probe  func(ctx context.Context, m catalog.Member) error
	Cache  *Cache // advisory; may be nil
}

// Resolve looks up a file id. homeHint is the recorded home member ("" if
// unknown). It returns a Result plus a plain error; the command boundary maps
// the Outcome to a tserr code. A non-nil error is a genuine failure (e.g. a
// Querier surfacing wal.ErrChainBroken) — distinct from the PartialView/Missing
// outcomes, which are normal classified results.
func (r *Resolver) Resolve(ctx context.Context, id, homeHint string) (Result, error) {
	active := r.Roster.Active()

	// Home-hint fast path: probe + query only the home first. The overwhelmingly
	// common success then costs a single member instead of the whole roster.
	if home, ok := findMember(active, homeHint); ok {
		reach := Probe(ctx, []catalog.Member{home}, r.Probe)
		if reach.AllAnswered() {
			view, err := r.Q.Query(ctx, home, id)
			if err != nil {
				return Result{}, err
			}
			if view.Found {
				return Result{Outcome: FoundAtHome, View: view, Home: homeHint, Reach: reach}, nil
			}
		}
		// Home missing or unreachable → fall through to a full fan-out. The
		// resolver's scope is the whole roster (D27, ls/search class).
	}

	return r.fanout(ctx, id, homeHint, active)
}

func (r *Resolver) fanout(ctx context.Context, id, homeHint string, active []catalog.Member) (Result, error) {
	reach := Probe(ctx, active, r.Probe)

	views, err := r.queryMembers(ctx, id, reach.Answered)
	if err != nil {
		return Result{}, err
	}

	// (a) Found directly at a reachable member (views are in sorted member order
	//     → deterministic winner; one id has one home so ties don't arise).
	for _, v := range views {
		if v.Found {
			return Result{Outcome: foundOutcome(v.Member, homeHint), View: v, Home: homeHint, Reach: reach}, nil
		}
	}

	// (b) Follow moved_to forwarding pointers (a completed move's record on the
	//     source doubles as a pointer that finds files whose new home is offline).
	for _, v := range views {
		if v.MovedTo != "" {
			res, followed, ferr := r.follow(ctx, id, homeHint, v, reach)
			if ferr != nil {
				return Result{}, ferr
			}
			if followed {
				return res, nil
			}
		}
	}

	// (c) Not found among answers. Any unreachable member means we cannot prove
	//     absence → PartialView (the safety verdict).
	if reach.Partial() {
		return r.partial(id, homeHint, reach, ""), nil
	}

	// (d) All members reachable. An in-flight move keeps the file from being
	//     "missing" — it is mid-relocation, which is PartialView-class.
	for _, v := range views {
		if v.PendingMove {
			return r.partial(id, homeHint, reach, ""), nil
		}
	}

	// (e) All reachable, no forwarding pointer, no pending move → genuinely gone.
	return Result{Outcome: Missing, Home: homeHint, Reach: reach}, nil
}

// follow chases a chain of moved_to pointers starting from start.MovedTo.
// Returns (result, followed=true) when it reaches a terminal verdict (found at
// the destination, or PartialView because a destination is unreachable/unknown);
// (zero, false) when the pointer dead-ends at a reachable member that does not
// hold the file (caller continues classification). Bounded hops + a visited set
// guard against runaway and cyclic chains.
func (r *Resolver) follow(ctx context.Context, id, homeHint string, start MemberView, reach Reach) (Result, bool, error) {
	visited := map[string]bool{start.Member: true}
	dest := start.MovedTo

	for hop := 0; hop < maxMoveHops; hop++ {
		if visited[dest] {
			return Result{}, false, fmt.Errorf("fed: moved_to cycle resolving id %s at member %q", id, dest)
		}
		visited[dest] = true

		destMember, ok := r.Roster.Find(dest)
		if !ok {
			// Destination not in the roster — the pointer proves the file moved
			// but we cannot query it, so we cannot prove absence → PartialView.
			return r.partial(id, homeHint, reach, dest), true, nil
		}

		dreach := Probe(ctx, []catalog.Member{destMember}, r.Probe)
		if !dreach.AllAnswered() {
			// Destination offline: the forwarding pointer still proves the file
			// exists and moved → PartialView naming the pointer (findable later).
			return r.partial(id, homeHint, reach, dest), true, nil
		}

		v, err := r.Q.Query(ctx, destMember, id)
		if err != nil {
			return Result{}, false, err
		}
		if v.Found {
			return Result{Outcome: foundOutcome(v.Member, homeHint), View: v, Home: homeHint, Reach: reach}, true, nil
		}
		if v.MovedTo != "" {
			dest = v.MovedTo
			continue
		}
		// Reachable destination that does not hold the file and forwards nowhere:
		// a dead pointer — let the caller finish classification.
		return Result{}, false, nil
	}
	return Result{}, false, fmt.Errorf("fed: moved_to chain for id %s exceeded %d hops", id, maxMoveHops)
}

// queryMembers queries the named members concurrently and returns their views in
// the input order (reach.Answered is sorted, so the output is deterministic).
// The first query error aborts and propagates (e.g. wal.ErrChainBroken).
func (r *Resolver) queryMembers(ctx context.Context, id string, members []string) ([]MemberView, error) {
	type res struct {
		v   MemberView
		err error
		ok  bool
	}
	results := make([]res, len(members))
	var wg sync.WaitGroup
	for i, name := range members {
		m, ok := r.Roster.Find(name)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(i int, m catalog.Member) {
			defer wg.Done()
			v, err := r.Q.Query(ctx, m, id)
			results[i] = res{v: v, err: err, ok: true}
		}(i, m)
	}
	wg.Wait()

	views := make([]MemberView, 0, len(members))
	for _, rr := range results {
		if !rr.ok {
			continue
		}
		if rr.err != nil {
			return nil, rr.err
		}
		views = append(views, rr.v)
	}
	return views, nil
}

// partial builds a PartialView result, attaching advisory cache coloring and,
// when pointer != "", naming the forwarding destination (and accounting it as
// unreachable). The Outcome is PartialView regardless of the cache — caches
// color, never decide.
func (r *Resolver) partial(id, homeHint string, reach Reach, pointer string) Result {
	res := Result{Outcome: PartialView, Home: homeHint, Reach: reach}
	if pointer != "" {
		res.View = MemberView{MovedTo: pointer}
		res.Reach = withUnreachable(reach, pointer)
	}
	if r.Cache != nil {
		if ms, ok := r.Cache.lastKnown(id); ok {
			res.LastSeen = ms
		}
	}
	return res
}

// foundOutcome decides FoundAtHome vs FoundElsewhere by whether the answering
// member is the recorded home.
func foundOutcome(member, homeHint string) Outcome {
	if member == homeHint {
		return FoundAtHome
	}
	return FoundElsewhere
}

func findMember(members []catalog.Member, name string) (catalog.Member, bool) {
	if name == "" {
		return catalog.Member{}, false
	}
	for _, m := range members {
		if m.Name == name {
			return m, true
		}
	}
	return catalog.Member{}, false
}

// withUnreachable returns a copy of reach with name added to Unreachable (if not
// already accounted), keeping the slices sorted and unshared.
func withUnreachable(reach Reach, name string) Reach {
	if contains(reach.Unreachable, name) || contains(reach.Answered, name) {
		return reach
	}
	out := Reach{
		Required:    reach.Required,
		Answered:    reach.Answered,
		Unreachable: append(append([]string(nil), reach.Unreachable...), name),
	}
	if !contains(out.Required, name) {
		out.Required = append(append([]string(nil), reach.Required...), name)
		sort.Strings(out.Required)
	}
	sort.Strings(out.Unreachable)
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
