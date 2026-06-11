// Package ops is the active counterpart to the WAL's passive pending/failed
// surfacing: it sweeps the write-ahead logs of all reachable federation members,
// lists not-done ops with their age, type, actor, blob refs and per-blob
// dependency relationships, and retries them client-driven over the backend
// (nodes never execute anything themselves — a retry replays the op's remaining
// steps idempotently, op-id dedupe making a double retry harmless).
//
// Listing tolerates partial reachability (unreachable members are reported, not
// fatal — its scope is "whoever answers"); a chain-broken member's ops are
// WITHHELD and surfaced as a trailing MemberStatus row (a tampered journal must
// never drive retries) while other members still list. `ops list` therefore
// reports the broken member and still exits 0 — the broken chain degrades, never
// fails, the listing. A broken chain only becomes a hard failure when a RETRY is
// attempted against that member: the retry is refused. Per the §8 layering rule
// this package returns plain errors; the command maps a refused-retry chain break
// to tserr (TV-FED-03, exit 6).
package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// Probe is the member-liveness seam (same shape as fed's prober) — injected so
// tests never make a real network call.
type Probe = func(ctx context.Context, m catalog.Member) error

// MemberWAL fetches one member's chain-verified WAL records over its backend.
// A wal.ErrChainBroken from Read marks that member's journal as tampered; its
// ops are withheld from the sweep.
type MemberWAL interface {
	Read(ctx context.Context, m catalog.Member) ([]wal.Rec, error)
}

// Verdict classifies whether an op can be mechanically retried.
type Verdict int

const (
	// Retryable: preconditions hold; replaying the remaining steps is safe.
	Retryable Verdict = iota
	// Unresolvable: preconditions are gone (source bytes lost, destination
	// removed, chain anchor broken) — needs physical fixing, never blind retry.
	Unresolvable
)

func (v Verdict) String() string {
	switch v {
	case Retryable:
		return "retryable"
	case Unresolvable:
		return "unresolvable"
	default:
		return fmt.Sprintf("Verdict(%d)", int(v))
	}
}

// PendingOp is one not-done (intent | failed) WAL entry, enriched for display
// and retry ordering.
type PendingOp struct {
	Member    string
	Rec       wal.Rec
	Age       time.Duration
	WaitsOn   []string // op ids ahead of it on a SHARED blob (per-blob ordering only)
	Verdict   Verdict  // filled by a Diagnose pass; defaults to Retryable
	Diagnosis string   // human text, set for Unresolvable
}

// MemberStatus reports one member's sweep outcome so the command can render
// "unreachable — ops unknown" / "chain broken — ops withheld" trailing rows.
type MemberStatus struct {
	Member      string
	Reachable   bool
	ChainBroken bool // wal.ErrChainBroken on Read (TV-FED-03 class); ops withheld
	Err         error
}

// SweepResult bundles the listing, reachability accounting and per-member status.
// (A struct rather than the sketch's bare tuple so per-member chain-broken state
// has a home — the sketch's 3-value return could not carry it.)
type SweepResult struct {
	Ops     []PendingOp
	Reach   fed.Reach
	Members []MemberStatus
}

// Executor replays one op's remaining steps for its op type. Implementations are
// registered per op type; each MUST be idempotent (already-completed steps are
// detected via op-id dedupe / Stat-before-write and skipped) and MUST end by
// writing the op's terminal WAL marker (done/failed).
type Executor interface {
	Diagnose(ctx context.Context, op PendingOp) (Verdict, string, error)
	Retry(ctx context.Context, op PendingOp) error
}

// Registry maps an op_type to its Executor (e.g. "ingest", "move", "gc").
type Registry map[string]Executor

// Sweep probes the roster's active members and reads each reachable member's
// chain-verified WAL, returning every not-done op plus reachability + per-member
// status. Unreachable and chain-broken members are reported, never fatal.
func Sweep(ctx context.Context, roster fed.Roster, q MemberWAL, probe Probe) (SweepResult, error) {
	active := roster.Active()
	reach := fed.Probe(ctx, active, probe)

	res := SweepResult{Reach: reach}
	byName := make(map[string]catalog.Member, len(active))
	for _, m := range active {
		byName[m.Name] = m
	}

	// Unreachable members: report, don't read.
	for _, name := range reach.Unreachable {
		res.Members = append(res.Members, MemberStatus{Member: name, Reachable: false})
	}

	// Reachable members: read + verify each WAL. A chain break withholds that
	// member's ops but never aborts the sweep.
	for _, name := range reach.Answered {
		m := byName[name]
		recs, err := q.Read(ctx, m)
		if err != nil {
			st := MemberStatus{Member: name, Reachable: true, Err: err}
			if isChainBroken(err) {
				st.ChainBroken = true
				res.Members = append(res.Members, st)
				continue // withhold this member's ops
			}
			// A non-chain read error is reported but also non-fatal.
			res.Members = append(res.Members, st)
			continue
		}
		res.Members = append(res.Members, MemberStatus{Member: name, Reachable: true})
		res.Ops = append(res.Ops, pendingFromRecs(name, recs)...)
	}
	return res, nil
}

// pendingFromRecs extracts the not-done ops of one member and computes each op's
// WaitsOn (earlier-seq pending ops sharing a blob ref — per-blob ordering only,
// there is no general dependency DAG).
func pendingFromRecs(member string, recs []wal.Rec) []PendingOp {
	// Order by seq so "earlier" is well-defined.
	sort.Slice(recs, func(i, j int) bool { return recs[i].Entry.Seq < recs[j].Entry.Seq })

	now := nowFunc()
	var out []PendingOp
	for i, r := range recs {
		if r.State == wal.StateDone {
			continue
		}
		op := PendingOp{
			Member: member,
			Rec:    r,
			Age:    now.Sub(r.Entry.CreatedAt),
		}
		// WaitsOn: earlier not-done ops in this member sharing any blob ref.
		for j := 0; j < i; j++ {
			pre := recs[j]
			if pre.State == wal.StateDone {
				continue
			}
			if sharesBlob(pre.Entry.BlobRefs, r.Entry.BlobRefs) {
				op.WaitsOn = append(op.WaitsOn, pre.Entry.OpID)
			}
		}
		out = append(out, op)
	}
	return out
}

// Retry diagnoses then re-runs a single op on its home member. It refuses an op
// whose WaitsOn predecessor is still pending (retry the head first) and an op the
// executor judges Unresolvable (a blind retry against missing preconditions is
// exactly the partial-failure class the WAL exists to prevent).
func Retry(ctx context.Context, op PendingOp, ex Executor) error {
	if len(op.WaitsOn) > 0 {
		return fmt.Errorf("ops: op %s waits on %v on the same blob — retry the head op first",
			shortID(op.Rec.Entry.OpID), op.WaitsOn)
	}
	verdict, diag, err := ex.Diagnose(ctx, op)
	if err != nil {
		return err
	}
	if verdict == Unresolvable {
		return fmt.Errorf("ops: op %s is unresolvable — %s", shortID(op.Rec.Entry.OpID), diag)
	}
	return ex.Retry(ctx, op)
}

// nowFunc is overridable in tests for deterministic ages.
var nowFunc = time.Now

// shortID is the 12-hex display form of an op id (like a git short SHA).
func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func sharesBlob(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func isChainBroken(err error) bool {
	return errors.Is(err, wal.ErrChainBroken)
}
