package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/ingest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/ops"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// newOpsCmd builds `tailvault ops` — list and retry pending/failed federation
// WAL ops across reachable members. Bare `ops` lists (the common inspection);
// `ops retry` re-runs ops client-driven over the backend (nodes execute nothing).
func newOpsCmd() *cobra.Command {
	var jsonOut, failPending bool
	var member string
	cmd := &cobra.Command{
		Use:   "ops",
		Short: "List and retry pending/failed federation WAL ops",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runOpsList(cmd, member, jsonOut, failPending)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the op listing as JSON")
	cmd.Flags().BoolVar(&failPending, "fail-pending", false, "exit non-zero if any op is pending/failed (for scripts/CI)")
	cmd.Flags().StringVar(&member, "member", "", "limit to a single member by name")

	list := &cobra.Command{
		Use:   "list",
		Short: "List pending/failed WAL ops across reachable members",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runOpsList(cmd, member, jsonOut, failPending)
		},
	}
	list.Flags().BoolVar(&jsonOut, "json", false, "emit the op listing as JSON")
	list.Flags().BoolVar(&failPending, "fail-pending", false, "exit non-zero if any op is pending/failed")
	list.Flags().StringVar(&member, "member", "", "limit to a single member by name")

	var all bool
	retry := &cobra.Command{
		Use:   "retry (<op-id> | --all)",
		Short: "Re-run pending/failed ops (client-driven, idempotent)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOpsRetry(cmd, args, all, member)
		},
	}
	retry.Flags().BoolVar(&all, "all", false, "retry all retryable ops (per-blob head-first order)")
	retry.Flags().StringVar(&member, "member", "", "limit to a single member by name")

	cmd.AddCommand(list, retry)
	return cmd
}

// registryMemberWAL implements ops.MemberWAL by reading a member's chain-verified
// WAL over the backend resolved from the user's locations registry.
type registryMemberWAL struct {
	bf func(catalog.Member) (backend.Backend, error)
}

func (q registryMemberWAL) Read(ctx context.Context, m catalog.Member) ([]wal.Rec, error) {
	b, err := q.bf(m)
	if err != nil {
		return nil, err
	}
	return (&wal.Log{B: b}).Read(ctx)
}

// sweepOps loads the roster + registry and sweeps all reachable members.
func sweepOps(ctx context.Context) (ops.SweepResult, locations.Registry, error) {
	reg, err := locations.Load()
	if err != nil {
		return ops.SweepResult{}, locations.Registry{}, tserr.ConfigErr("ops: load locations.toml", err)
	}
	roster, err := loadRoster(ctx, reg)
	if err != nil {
		return ops.SweepResult{}, reg, err // already a typed config error
	}
	q := registryMemberWAL{bf: backendForRegistry(reg)}
	res, err := ops.Sweep(ctx, roster, q, memberProbe(reg))
	return res, reg, err
}

func runOpsList(cmd *cobra.Command, member string, jsonOut, failPending bool) error {
	ctx := cmd.Context()
	res, reg, err := sweepOps(ctx)
	if err != nil {
		return err
	}
	opsList := filterByMember(res.Ops, member)
	out := cmd.OutOrStdout()

	if jsonOut {
		if err := emitOpsJSON(ctx, out, reg, res, opsList); err != nil {
			return err
		}
	} else {
		now := time.Now()
		writeOpsTable(ctx, out, reg, res, opsList, member, now)
	}
	if failPending && len(opsList) > 0 {
		// A plain error → exit 1 at the boundary (not a tserr bucket); scripts use this.
		return fmt.Errorf("ops: %d pending/failed op(s)", len(opsList))
	}
	return nil
}

func writeOpsTable(ctx context.Context, out io.Writer, reg locations.Registry, res ops.SweepResult, opsList []ops.PendingOp, member string, now time.Time) {
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "MEMBER\tOP-ID\tTYPE\tSTATE\tAGE\tBLOBS\tWAITS-ON\tVERDICT")
	for _, op := range opsList {
		verdict := diagnoseVerdict(ctx, reg, op)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			op.Member, identity.Short(op.Rec.Entry.OpID), op.Rec.Entry.OpType, op.Rec.State,
			ageString(now, op.Rec.Entry.CreatedAt), shortBlobs(op.Rec.Entry.BlobRefs),
			shortIDs(op.WaitsOn), verdict)
	}
	// Trailing rows for members that did not fully answer.
	for _, ms := range res.Members {
		if member != "" && ms.Member != member {
			continue
		}
		switch {
		case ms.ChainBroken:
			fmt.Fprintf(tw, "%s\t\t\t\t\t\t\tchain broken — ops withheld (TV-FED-03)\n", ms.Member)
		case !ms.Reachable:
			fmt.Fprintf(tw, "%s\t\t\t\t\t\t\tunreachable — ops unknown\n", ms.Member)
		}
	}
	tw.Flush()
	fmt.Fprintf(out, "ops: %d pending/failed across %d reachable member(s)\n", len(opsList), len(res.Reach.Answered))
}

// opJSON is the --json shape for one op.
type opJSON struct {
	Member  string   `json:"member"`
	OpID    string   `json:"op_id"`
	Type    string   `json:"type"`
	State   string   `json:"state"`
	AgeSecs int64    `json:"age_secs"`
	Blobs   []string `json:"blobs"`
	WaitsOn []string `json:"waits_on"`
	Verdict string   `json:"verdict"`
}

func emitOpsJSON(ctx context.Context, out io.Writer, reg locations.Registry, res ops.SweepResult, opsList []ops.PendingOp) error {
	now := time.Now()
	payload := struct {
		Ops          []opJSON `json:"ops"`
		Answered     []string `json:"answered"`
		Unreachable  []string `json:"unreachable"`
		ChainBrokenM []string `json:"chain_broken"`
	}{Answered: res.Reach.Answered, Unreachable: res.Reach.Unreachable}
	for _, ms := range res.Members {
		if ms.ChainBroken {
			payload.ChainBrokenM = append(payload.ChainBrokenM, ms.Member)
		}
	}
	for _, op := range opsList {
		payload.Ops = append(payload.Ops, opJSON{
			Member: op.Member, OpID: op.Rec.Entry.OpID, Type: op.Rec.Entry.OpType,
			State: string(op.Rec.State), AgeSecs: int64(now.Sub(op.Rec.Entry.CreatedAt).Seconds()),
			Blobs: op.Rec.Entry.BlobRefs, WaitsOn: op.WaitsOn, Verdict: diagnoseVerdict(ctx, reg, op).String(),
		})
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func runOpsRetry(cmd *cobra.Command, args []string, all bool, member string) error {
	ctx := cmd.Context()
	if all == (len(args) == 1) {
		return tserr.ConfigErr("ops retry: give exactly one of <op-id> or --all", nil)
	}
	res, reg, err := sweepOps(ctx)
	if err != nil {
		return err
	}
	candidates := filterByMember(res.Ops, member)

	var targets []ops.PendingOp
	if all {
		// Head-first: retry ops with no pending WaitsOn predecessor first; ops.Retry
		// refuses any op whose predecessor is still pending, so a single pass that
		// skips blocked ops is correct (re-run to drain a chain).
		targets = candidates
	} else {
		op, err := findByPrefix(candidates, strings.ToLower(args[0]))
		if err != nil {
			return err
		}
		targets = []ops.PendingOp{op}
	}

	out := cmd.OutOrStdout()
	var failed int
	for _, op := range targets {
		ex, err := executorFor(ctx, reg, op)
		if err != nil {
			fmt.Fprintf(out, "%s (%s) on %s: %v\n", identity.Short(op.Rec.Entry.OpID), op.Rec.Entry.OpType, op.Member, err)
			failed++
			continue
		}
		if err := ops.Retry(ctx, op, ex); err != nil {
			fmt.Fprintf(out, "%s (%s) on %s: %v\n", identity.Short(op.Rec.Entry.OpID), op.Rec.Entry.OpType, op.Member, err)
			failed++
			continue
		}
		fmt.Fprintf(out, "%s (%s) on %s: done\n", identity.Short(op.Rec.Entry.OpID), op.Rec.Entry.OpType, op.Member)
	}
	if failed > 0 {
		return fmt.Errorf("ops retry: %d op(s) failed or were refused", failed)
	}
	return nil
}

// executorFor builds the ops.Executor for one op's type on its member: the gc
// executor (mine, works over any backend) or the ingest.ReplayOp dispatcher for
// ingest/scan/move/delete (local-root only — it persists the catalog via local
// WriteAtomic, so SSH-member replay is deferred until SSH bootstrap lands,
// DG-33.1). Block-4 op types (roster/sync_mode) register their own executors.
func executorFor(ctx context.Context, reg locations.Registry, op ops.PendingOp) (ops.Executor, error) {
	loc, ok := reg.Locations[op.Member]
	if !ok {
		return nil, tserr.ConfigErr(fmt.Sprintf("member %q not in locations.toml — run `tailvault location add %s`", op.Member, op.Member), nil)
	}
	b, err := backendForLocation(loc, op.Member)
	if err != nil {
		return nil, err
	}
	cat, err := readCatalog(ctx, b)
	if err != nil {
		return nil, err
	}
	if cat == nil {
		cat = &catalog.Catalog{Version: 2, VaultName: op.Member, Node: loc.Node}
	}
	log := &wal.Log{B: b}

	switch op.Rec.Entry.OpType {
	case wal.OpGC:
		return &gcOpExecutor{be: b, cat: cat, log: log}, nil
	case wal.OpIngest, wal.OpScan, wal.OpMove, wal.OpDelete:
		if loc.Backend != locations.BackendTaildrive {
			return nil, tserr.ConfigErr(fmt.Sprintf("retry of a %s op on SSH member %q is not yet supported (remote catalog replay; DG-33.1) — fix on the node", op.Rec.Entry.OpType, op.Member), nil)
		}
		catPath := filepath.Join(loc.BasePath, "meta", "catalog.toml")
		return &replayOpExecutor{log: log, cat: cat, catPath: catPath, node: loc.Node}, nil
	default:
		return nil, tserr.ConfigErr(fmt.Sprintf("no executor registered for op type %q", op.Rec.Entry.OpType), nil)
	}
}

// diagnoseVerdict runs the op's executor Diagnose for the listing VERDICT column,
// degrading to a readable note when no executor applies (e.g. SSH ingest replay).
func diagnoseVerdict(ctx context.Context, reg locations.Registry, op ops.PendingOp) ops.Verdict {
	ex, err := executorFor(ctx, reg, op)
	if err != nil {
		return ops.Unresolvable
	}
	v, _, derr := ex.Diagnose(ctx, op)
	if derr != nil {
		return ops.Unresolvable
	}
	return v
}

// --- executors (cli layer, cycle-free) ---

// replayOpExecutor replays an ingest/scan/move/delete op through ingest.ReplayOp
// (the same engine code that created it — never a parallel implementation).
type replayOpExecutor struct {
	log     *wal.Log
	cat     *catalog.Catalog
	catPath string
	node    string
}

func (e *replayOpExecutor) Diagnose(_ context.Context, _ ops.PendingOp) (ops.Verdict, string, error) {
	// ReplayOp reconstructs the catalog mutation from the IMMUTABLE entry and is
	// idempotent, so these ops are retryable; a genuine precondition loss surfaces
	// when ReplayOp runs (it returns the error and leaves the op pending).
	return ops.Retryable, "", nil
}

func (e *replayOpExecutor) Retry(ctx context.Context, op ops.PendingOp) error {
	return ingest.ReplayOp(ctx, e.log, e.cat, e.catPath, e.node, op.Rec, time.Now)
}

// gcOpExecutor finishes a journaled gc op: delete the doomed blobs (idempotent —
// already-deleted ids are skipped), update the catalog atomically, then MarkDone.
// It does not re-plan (the doomed set is fixed in the op's blob_refs).
type gcOpExecutor struct {
	be  backend.Backend
	cat *catalog.Catalog
	log *wal.Log
}

func (e *gcOpExecutor) Diagnose(_ context.Context, _ ops.PendingOp) (ops.Verdict, string, error) {
	return ops.Retryable, "", nil // deletes are idempotent (rm -f)
}

func (e *gcOpExecutor) Retry(ctx context.Context, op ops.PendingOp) error {
	for _, id := range op.Rec.Entry.BlobRefs {
		f, ok := e.cat.FindID(id)
		if !ok {
			continue // already removed by the original sweep — idempotent skip
		}
		if err := e.be.Delete(ctx, "objects/"+f.SHA256); err != nil {
			_ = e.log.MarkFailed(ctx, op.Rec.Entry.OpID, err.Error())
			return err
		}
		e.cat.Remove(f.Path)
	}
	bs, err := catalog.Encode(e.cat)
	if err != nil {
		_ = e.log.MarkFailed(ctx, op.Rec.Entry.OpID, err.Error())
		return err
	}
	if err := e.be.PutOverwrite(ctx, "meta/catalog.toml", bytes.NewReader(bs)); err != nil {
		_ = e.log.MarkFailed(ctx, op.Rec.Entry.OpID, err.Error())
		return err
	}
	return e.log.MarkDone(ctx, op.Rec.Entry.OpID)
}

// --- helpers ---

func filterByMember(in []ops.PendingOp, member string) []ops.PendingOp {
	if member == "" {
		return in
	}
	var out []ops.PendingOp
	for _, op := range in {
		if op.Member == member {
			out = append(out, op)
		}
	}
	return out
}

// findByPrefix resolves a 12-hex (or longer) op-id prefix to exactly one op,
// erroring on zero or ambiguous matches.
func findByPrefix(in []ops.PendingOp, prefix string) (ops.PendingOp, error) {
	var matches []ops.PendingOp
	for _, op := range in {
		if strings.HasPrefix(strings.ToLower(op.Rec.Entry.OpID), prefix) {
			matches = append(matches, op)
		}
	}
	switch len(matches) {
	case 0:
		return ops.PendingOp{}, tserr.ConfigErr("ops retry: no pending op matches "+prefix, nil)
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for _, op := range matches {
			ids = append(ids, identity.Short(op.Rec.Entry.OpID))
		}
		sort.Strings(ids)
		return ops.PendingOp{}, tserr.ConfigErr("ops retry: ambiguous op-id prefix "+prefix+" matches "+strings.Join(ids, ", "), nil)
	}
}

func ageString(now, then time.Time) string {
	d := now.Sub(then)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func shortBlobs(ids []string) string {
	if len(ids) == 0 {
		return "-"
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = identity.Short(id)
	}
	return strings.Join(out, ",")
}

func shortIDs(ids []string) string {
	if len(ids) == 0 {
		return "-"
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = identity.Short(id)
	}
	return strings.Join(out, ",")
}
