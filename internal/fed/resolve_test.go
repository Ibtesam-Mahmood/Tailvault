package fed

import (
	"context"
	"errors"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
)

// stubQuerier answers from an in-memory member→id→view table; an unknown id on a
// member yields a not-found view (the realistic "I don't hold it" answer).
type stubQuerier struct {
	views map[string]map[string]MemberView
	err   error // if set, Query returns this for every call (chain-broken sim)
}

func (s stubQuerier) Query(_ context.Context, m catalog.Member, id string) (MemberView, error) {
	if s.err != nil {
		return MemberView{}, s.err
	}
	if byID, ok := s.views[m.Name]; ok {
		if v, ok := byID[id]; ok {
			v.Member = m.Name
			return v, nil
		}
	}
	return MemberView{Member: m.Name, Found: false}, nil
}

func proberDown(down ...string) func(context.Context, catalog.Member) error {
	set := make(map[string]bool, len(down))
	for _, d := range down {
		set[d] = true
	}
	return func(_ context.Context, m catalog.Member) error {
		if set[m.Name] {
			return errors.New("node offline")
		}
		return nil
	}
}

func roster(members ...catalog.Member) Roster {
	r := Roster{FedID: "fed-1", Members: members}
	r.sortMembers()
	return r
}

func found(id string) MemberView { return MemberView{Found: true, File: catalog.File{ID: id}} }

const tid = "30092d830e2641b447745655bbe4171675720a1aa8cf80e0ae3736e6e43111f0"

func newResolver(r Roster, q Querier, down ...string) *Resolver {
	return &Resolver{Roster: r, Q: q, Probe: proberDown(down...)}
}

func member(name string) catalog.Member { return mem(name, "active", "2026-06-11T09:00:00Z") }

func TestResolve_FoundAtHome(t *testing.T) {
	r := roster(member("pi-1"), member("pi-2"))
	q := stubQuerier{views: map[string]map[string]MemberView{
		"pi-1": {tid: found(tid)},
	}}
	res, err := newResolver(r, q).Resolve(context.Background(), tid, "pi-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != FoundAtHome {
		t.Errorf("Outcome = %s, want found-at-home", res.Outcome)
	}
	if res.View.Member != "pi-1" {
		t.Errorf("winning member = %q, want pi-1", res.View.Member)
	}
	// Home-hint fast path probes only the home.
	if !eqStrs(res.Reach.Required, []string{"pi-1"}) {
		t.Errorf("Reach.Required = %v, want [pi-1] (fast path)", res.Reach.Required)
	}
}

func TestResolve_FoundElsewhere(t *testing.T) {
	r := roster(member("pi-1"), member("pi-2"))
	q := stubQuerier{views: map[string]map[string]MemberView{
		"pi-2": {tid: found(tid)}, // home is pi-1 but the file lives on pi-2
	}}
	res, err := newResolver(r, q).Resolve(context.Background(), tid, "pi-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != FoundElsewhere {
		t.Errorf("Outcome = %s, want found-elsewhere", res.Outcome)
	}
	if res.View.Member != "pi-2" {
		t.Errorf("winning member = %q, want pi-2", res.View.Member)
	}
	if !eqStrs(res.Reach.Required, []string{"pi-1", "pi-2"}) {
		t.Errorf("Reach.Required = %v, want full roster", res.Reach.Required)
	}
}

func TestResolve_PartialView_DownMember(t *testing.T) {
	r := roster(member("pi-1"), member("pi-2"))
	q := stubQuerier{} // nobody holds it
	res, err := newResolver(r, q, "pi-2").Resolve(context.Background(), tid, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != PartialView {
		t.Errorf("Outcome = %s, want partial-view", res.Outcome)
	}
	if !eqStrs(res.Reach.Unreachable, []string{"pi-2"}) {
		t.Errorf("Reach.Unreachable = %v, want [pi-2]", res.Reach.Unreachable)
	}
}

func TestResolve_Missing_AllReachable(t *testing.T) {
	r := roster(member("pi-1"), member("pi-2"))
	q := stubQuerier{} // nobody holds it, all up
	res, err := newResolver(r, q).Resolve(context.Background(), tid, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Missing {
		t.Errorf("Outcome = %s, want missing", res.Outcome)
	}
	if !res.Reach.AllAnswered() {
		t.Errorf("expected all answered; Unreachable=%v", res.Reach.Unreachable)
	}
}

func TestResolve_MovedTo_ReachableDest(t *testing.T) {
	r := roster(member("pi-1"), member("pi-2"))
	q := stubQuerier{views: map[string]map[string]MemberView{
		"pi-1": {tid: {MovedTo: "pi-2"}},
		"pi-2": {tid: found(tid)},
	}}
	res, err := newResolver(r, q).Resolve(context.Background(), tid, "pi-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != FoundElsewhere || res.View.Member != "pi-2" {
		t.Errorf("got %s @ %q, want found-elsewhere @ pi-2", res.Outcome, res.View.Member)
	}
}

func TestResolve_MovedTo_DestDown(t *testing.T) {
	r := roster(member("pi-1"), member("pi-2"))
	q := stubQuerier{views: map[string]map[string]MemberView{
		"pi-1": {tid: {MovedTo: "pi-2"}},
	}}
	res, err := newResolver(r, q, "pi-2").Resolve(context.Background(), tid, "pi-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != PartialView {
		t.Fatalf("Outcome = %s, want partial-view", res.Outcome)
	}
	if res.View.MovedTo != "pi-2" {
		t.Errorf("partial view should name the forwarding dest; View.MovedTo=%q", res.View.MovedTo)
	}
}

func TestResolve_MovedTo_TwoHop(t *testing.T) {
	r := roster(member("pi-1"), member("pi-2"), member("pi-3"))
	q := stubQuerier{views: map[string]map[string]MemberView{
		"pi-1": {tid: {MovedTo: "pi-2"}},
		"pi-2": {tid: {MovedTo: "pi-3"}},
		"pi-3": {tid: found(tid)},
	}}
	res, err := newResolver(r, q).Resolve(context.Background(), tid, "pi-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != FoundElsewhere || res.View.Member != "pi-3" {
		t.Errorf("2-hop got %s @ %q, want found-elsewhere @ pi-3", res.Outcome, res.View.Member)
	}
}

func TestResolve_MovedTo_Cycle(t *testing.T) {
	r := roster(member("pi-1"), member("pi-2"))
	q := stubQuerier{views: map[string]map[string]MemberView{
		"pi-1": {tid: {MovedTo: "pi-2"}},
		"pi-2": {tid: {MovedTo: "pi-1"}},
	}}
	_, err := newResolver(r, q).Resolve(context.Background(), tid, "pi-1")
	if err == nil {
		t.Fatal("moved_to cycle should error, not hang or misclassify")
	}
}

func TestResolve_PendingMove_BlocksMissing(t *testing.T) {
	r := roster(member("pi-1"), member("pi-2"))
	q := stubQuerier{views: map[string]map[string]MemberView{
		"pi-1": {tid: {PendingMove: true}}, // in flight; not Found, all reachable
	}}
	res, err := newResolver(r, q).Resolve(context.Background(), tid, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != PartialView {
		t.Errorf("in-flight move must be partial-view, not %s", res.Outcome)
	}
}

func TestResolve_ChainBrokenPropagates(t *testing.T) {
	r := roster(member("pi-1"))
	q := stubQuerier{err: errors.New("wal: hash chain verification failed")}
	_, err := newResolver(r, q).Resolve(context.Background(), tid, "pi-1")
	if err == nil {
		t.Fatal("a Querier error (chain-broken) must propagate, not be swallowed")
	}
}

func TestResolve_CacheColorsPartialView(t *testing.T) {
	r := roster(member("pi-1"), member("pi-2"))
	q := stubQuerier{} // nobody holds it
	cache := &Cache{Dir: t.TempDir()}
	if err := cache.Record(snap("fed-1", summary("pi-2", tid))); err != nil {
		t.Fatal(err)
	}
	res := &Result{}
	resolver := &Resolver{Roster: r, Q: q, Probe: proberDown("pi-2"), Cache: cache}
	*res, _ = resolver.Resolve(context.Background(), tid, "")
	if res.Outcome != PartialView {
		t.Fatalf("want partial-view, got %s", res.Outcome)
	}
	if res.LastSeen == nil || res.LastSeen.Name != "pi-2" {
		t.Errorf("cache should color LastSeen with pi-2; got %+v", res.LastSeen)
	}

	// Without the cache, the Outcome is identical and LastSeen is nil.
	res2, _ := newResolver(r, q, "pi-2").Resolve(context.Background(), tid, "")
	if res2.Outcome != PartialView {
		t.Errorf("Outcome must not depend on the cache; got %s", res2.Outcome)
	}
	if res2.LastSeen != nil {
		t.Errorf("no cache → LastSeen should be nil; got %+v", res2.LastSeen)
	}
}

func TestResolve_ReachMetadataExact(t *testing.T) {
	r := roster(member("a"), member("b"), member("c"))
	q := stubQuerier{}
	res, err := newResolver(r, q, "b").Resolve(context.Background(), tid, "")
	if err != nil {
		t.Fatal(err)
	}
	if !eqStrs(res.Reach.Required, []string{"a", "b", "c"}) {
		t.Errorf("Required = %v", res.Reach.Required)
	}
	if !eqStrs(res.Reach.Answered, []string{"a", "c"}) {
		t.Errorf("Answered = %v, want [a c]", res.Reach.Answered)
	}
	if !eqStrs(res.Reach.Unreachable, []string{"b"}) {
		t.Errorf("Unreachable = %v, want [b]", res.Reach.Unreachable)
	}
}
