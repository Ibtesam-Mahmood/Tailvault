package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

var bg = context.Background()

func m(name string) catalog.Member {
	return catalog.Member{Name: name, Node: name + ".ts.net", JoinedAt: time.Unix(0, 0).UTC(), Status: "active"}
}

func roster(names ...string) fed.Roster {
	r := fed.Roster{FedID: "fed-1"}
	for _, n := range names {
		r.Members = append(r.Members, m(n))
	}
	return r
}

func prober(down ...string) Probe {
	set := map[string]bool{}
	for _, d := range down {
		set[d] = true
	}
	return func(_ context.Context, mm catalog.Member) error {
		if set[mm.Name] {
			return errors.New("down")
		}
		return nil
	}
}

func rec(seq uint64, opID, opType string, state wal.State, blobs ...string) wal.Rec {
	return wal.Rec{
		Entry: wal.Entry{Seq: seq, OpID: opID, OpType: opType, BlobRefs: blobs, CreatedAt: time.Unix(1000, 0).UTC()},
		State: state,
	}
}

type stubWAL struct {
	recs map[string][]wal.Rec
	errs map[string]error
}

func (s stubWAL) Read(_ context.Context, mm catalog.Member) ([]wal.Rec, error) {
	if e := s.errs[mm.Name]; e != nil {
		return nil, e
	}
	return s.recs[mm.Name], nil
}

type stubExec struct {
	verdict  Verdict
	diag     string
	diagErr  error
	retried  bool
	retryErr error
}

func (e *stubExec) Diagnose(_ context.Context, _ PendingOp) (Verdict, string, error) {
	return e.verdict, e.diag, e.diagErr
}
func (e *stubExec) Retry(_ context.Context, _ PendingOp) error {
	e.retried = true
	return e.retryErr
}

func findOp(ops []PendingOp, opID string) (PendingOp, bool) {
	for _, o := range ops {
		if o.Rec.Entry.OpID == opID {
			return o, true
		}
	}
	return PendingOp{}, false
}

func memberStatus(res SweepResult, name string) (MemberStatus, bool) {
	for _, s := range res.Members {
		if s.Member == name {
			return s, true
		}
	}
	return MemberStatus{}, false
}

func TestSweep_ListsAcrossMembers(t *testing.T) {
	q := stubWAL{recs: map[string][]wal.Rec{
		"pi-1": {
			rec(0, "op1", wal.OpIngest, wal.StateIntent, "idA"),
			rec(1, "opDone", wal.OpIngest, wal.StateDone, "idC"), // done → excluded
		},
		"pi-2": {rec(0, "op2", wal.OpMove, wal.StateFailed, "idB")},
	}}
	res, err := Sweep(bg, roster("pi-1", "pi-2", "pi-3"), q, prober("pi-3"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ops) != 2 {
		t.Fatalf("want 2 not-done ops, got %d (%+v)", len(res.Ops), res.Ops)
	}
	if _, ok := findOp(res.Ops, "opDone"); ok {
		t.Error("done op must be excluded from the listing")
	}
	// pi-3 is down → reported unreachable, not fatal.
	if st, ok := memberStatus(res, "pi-3"); !ok || st.Reachable {
		t.Errorf("pi-3 should be reported unreachable; got %+v", st)
	}
	if !res.Reach.Partial() {
		t.Error("reach should be partial (pi-3 down)")
	}
}

func TestSweep_PerBlobWaitsOn(t *testing.T) {
	q := stubWAL{recs: map[string][]wal.Rec{
		"pi-1": {
			rec(0, "opA", wal.OpIngest, wal.StateIntent, "idX"),
			rec(1, "opB", wal.OpMove, wal.StateIntent, "idX"),   // same blob → waits on opA
			rec(2, "opC", wal.OpIngest, wal.StateIntent, "idY"), // different blob → no edge
		},
	}}
	res, err := Sweep(bg, roster("pi-1"), q, prober())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := findOp(res.Ops, "opA")
	b, _ := findOp(res.Ops, "opB")
	c, _ := findOp(res.Ops, "opC")
	if len(a.WaitsOn) != 0 {
		t.Errorf("opA (head) WaitsOn = %v, want none", a.WaitsOn)
	}
	if len(b.WaitsOn) != 1 || b.WaitsOn[0] != "opA" {
		t.Errorf("opB WaitsOn = %v, want [opA]", b.WaitsOn)
	}
	if len(c.WaitsOn) != 0 {
		t.Errorf("opC (different blob) WaitsOn = %v, want none", c.WaitsOn)
	}
}

func TestSweep_ChainBrokenWithheld(t *testing.T) {
	q := stubWAL{
		recs: map[string][]wal.Rec{"pi-1": {rec(0, "op1", wal.OpIngest, wal.StateIntent, "idA")}},
		errs: map[string]error{"pi-2": wal.ErrChainBroken},
	}
	res, err := Sweep(bg, roster("pi-1", "pi-2"), q, prober())
	if err != nil {
		t.Fatal(err)
	}
	// pi-2's ops withheld; pi-1 still lists.
	if _, ok := findOp(res.Ops, "op1"); !ok {
		t.Error("pi-1's op must still list despite pi-2's chain break")
	}
	st, ok := memberStatus(res, "pi-2")
	if !ok || !st.ChainBroken {
		t.Errorf("pi-2 should be reported ChainBroken; got %+v", st)
	}
	for _, o := range res.Ops {
		if o.Member == "pi-2" {
			t.Error("a chain-broken member's ops must be withheld")
		}
	}
}

func TestRetry_Success(t *testing.T) {
	ex := &stubExec{verdict: Retryable}
	op := PendingOp{Rec: rec(0, "op1", wal.OpIngest, wal.StateIntent, "idA")}
	if err := Retry(bg, op, ex); err != nil {
		t.Fatal(err)
	}
	if !ex.retried {
		t.Error("a retryable op should be retried")
	}
}

func TestRetry_RefusedWhenWaitsOnPending(t *testing.T) {
	ex := &stubExec{verdict: Retryable}
	op := PendingOp{Rec: rec(1, "opB", wal.OpMove, wal.StateIntent, "idX"), WaitsOn: []string{"opA"}}
	if err := Retry(bg, op, ex); err == nil {
		t.Error("retry must be refused while a WaitsOn predecessor is pending")
	}
	if ex.retried {
		t.Error("a refused retry must not execute")
	}
}

func TestRetry_RefusedWhenUnresolvable(t *testing.T) {
	ex := &stubExec{verdict: Unresolvable, diag: "blob bytes lost — restore from a clone"}
	op := PendingOp{Rec: rec(0, "op1", wal.OpMove, wal.StateIntent, "idA")}
	err := Retry(bg, op, ex)
	if err == nil {
		t.Error("an unresolvable op must not be retried")
	}
	if ex.retried {
		t.Error("an unresolvable op must not execute Retry")
	}
}

func TestVerdictString(t *testing.T) {
	if Retryable.String() != "retryable" || Unresolvable.String() != "unresolvable" {
		t.Error("Verdict.String mismatch")
	}
}
