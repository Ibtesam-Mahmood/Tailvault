package gc

import (
	"context"
	"errors"
	"fmt"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// ErrNeedAllMembers is the sentinel for a federated gc that ran while ≥1 active
// member was unreachable. Deletion can never tolerate a partial view (D27/R3).
// The concrete error is a *NeedAllMembersError carrying the unreachable members;
// errors.Is(err, ErrNeedAllMembers) matches it, and the command boundary maps it
// to tserr.FedNeedAllMembersErr (exit 6) with the member list.
var ErrNeedAllMembers = errors.New("gc: federated gc requires all active members reachable")

// NeedAllMembersError carries the members that failed the all-members gate so the
// command boundary can name them in the TV-FED-02 message.
type NeedAllMembersError struct{ Unreachable []string }

func (e *NeedAllMembersError) Error() string {
	return fmt.Sprintf("%s: unreachable: %v", ErrNeedAllMembers.Error(), e.Unreachable)
}

// Unwrap makes errors.Is(err, ErrNeedAllMembers) hold.
func (e *NeedAllMembersError) Unwrap() error { return ErrNeedAllMembers }

// FedContext carries the federation inputs for a federated gc. The non-federated
// path keeps using the v1 PlanSweep/Sweep functions unchanged — these federated
// entry points are only invoked when a vault is federated, so non-federated gc
// sees zero behavior change.
type FedContext struct {
	Backend backend.Backend                                   // home node store (objects/ live here)
	Roster  fed.Roster                                        // federation roster
	Probe   func(ctx context.Context, m catalog.Member) error // member liveness seam (injected)
	Cat     *catalog.Catalog                                  // home node catalog (sync_mode scope + sha→id)
	Log     *wal.Log                                          // home node WAL (pending skip + journaling)
	Actor   string                                            // identity stamped on the gc WAL op

	// PersistCatalog writes the home catalog back after a sweep removes doomed
	// git-mode entries. It is injected because catalog-overwrite over a backend is
	// a command-layer concern (backend.Put dedups by key, so a single-key catalog
	// cannot be overwritten by Put alone), not gc's. nil skips the catalog update
	// (the sweep still journals + deletes).
	PersistCatalog func(ctx context.Context, c *catalog.Catalog) error
}

// PlanResult is a federated plan: the v1 Plan plus federation accounting.
type PlanResult struct {
	Plan                    // embedded v1 result: Eligible (bare shas) / Kept / Preserved
	DoomedIDs      []string // file ids of the Eligible shas — the gc WAL op's blob_refs
	SkippedPending []string // bare shas dropped because a pending WAL intent locks their file id
}

// Report is the outcome of a federated sweep.
type Report struct {
	Deleted        int
	SkippedPending []string // re-skipped at sweep time (a plan→sweep race)
	DryRun         bool
}

// PlanFederated applies the three federation gates around the v1 mark step, in
// this order (a failed gate aborts before any marking so a partial view can
// never shape a doomed set):
//
//  1. all-members reachability gate — every active member must answer
//     (else ErrNeedAllMembers, with NOTHING computed past the gate);
//  2. candidate scoping — only sync_mode=="git" objects are ever candidates
//     (allow-list, D14; manual/unknown modes are categorically un-collectable, D15);
//  3. keep-set subtraction (v1 union of branch-lock references) then
//     pending-intent skip by file id (D13).
func PlanFederated(ctx context.Context, fctx *FedContext, stored []string, keep, preserve KeepSet) (PlanResult, error) {
	if fctx == nil {
		return PlanResult{}, errors.New("gc: PlanFederated requires a FedContext (non-federated vaults use PlanSweep)")
	}

	// 1. Gate.
	reach := fed.Probe(ctx, fctx.Roster.Active(), fctx.Probe)
	if !reach.AllAnswered() {
		return PlanResult{}, &NeedAllMembersError{Unreachable: reach.Unreachable}
	}

	// 2. Candidate scoping to git-mode objects only.
	gitShas, shaToID := gitModeIndex(fctx.Cat)
	candidates := make([]string, 0, len(stored))
	for _, key := range stored {
		if gitShas.Has(StripObjectKey(key)) {
			candidates = append(candidates, key)
		}
	}

	// 3a. v1 keep-set subtraction over the git-mode candidate subset.
	plan := PlanSweep(candidates, keep, preserve)

	// 3b. Pending-intent skip (by file id).
	pend, err := pendingIDSet(ctx, fctx.Log)
	if err != nil {
		return PlanResult{}, err
	}
	res := PlanResult{Plan: plan}
	res.Eligible = nil
	for _, sha := range plan.Eligible {
		id := shaToID[sha]
		if id != "" && pend.Has(id) {
			res.SkippedPending = append(res.SkippedPending, sha)
			continue
		}
		res.Eligible = append(res.Eligible, sha)
		if id != "" {
			res.DoomedIDs = append(res.DoomedIDs, id)
		}
	}
	return res, nil
}

// SweepFederated executes a federated plan as a WAL-journaled op so a crashed
// sweep is visible and resumable like any other mutation: append a gc intent
// (blob_refs = doomed file ids — this makes gc participate in WAL-as-lock, so a
// pending gc blocks a concurrent move on the same blob and vice versa) → delete
// objects → update the catalog → mark done. It re-checks pending at sweep time
// (a plan→sweep race) and re-skips conflicts. dryRun runs the re-check and
// reports but mutates nothing.
func SweepFederated(ctx context.Context, fctx *FedContext, p PlanResult, dryRun bool) (Report, error) {
	rep := Report{DryRun: dryRun}
	if fctx == nil {
		return rep, errors.New("gc: SweepFederated requires a FedContext")
	}
	if len(p.Eligible) == 0 {
		return rep, nil
	}

	// Re-check pending at sweep time: drop any doomed id locked since planning.
	pend, err := pendingIDSet(ctx, fctx.Log)
	if err != nil {
		return rep, err
	}
	_, shaToID := gitModeIndex(fctx.Cat)
	var doomSha, doomID []string
	for _, sha := range p.Eligible {
		id := shaToID[sha]
		if id != "" && pend.Has(id) {
			rep.SkippedPending = append(rep.SkippedPending, sha)
			continue
		}
		doomSha = append(doomSha, sha)
		if id != "" {
			doomID = append(doomID, id)
		}
	}
	if len(doomSha) == 0 || dryRun {
		return rep, nil
	}

	// Journal the sweep: the gc intent locks the doomed ids (WAL-as-lock). If a
	// doomed id was locked by another op between the re-check and here, AppendIntent
	// returns ErrOpInFlight and we abort cleanly without deleting anything.
	op := wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpGC, BlobRefs: doomID, Actor: fctx.Actor}
	rec, err := fctx.Log.AppendIntent(ctx, op)
	if err != nil {
		return rep, err
	}

	// Delete the blobs.
	for _, sha := range doomSha {
		if err := fctx.Backend.Delete(ctx, objectPrefix+sha); err != nil {
			_ = fctx.Log.MarkFailed(ctx, rec.Entry.OpID, err.Error())
			return rep, fmt.Errorf("gc: delete %s%s: %w", objectPrefix, sha, err)
		}
		rep.Deleted++
	}

	// Update the catalog (remove the doomed git-mode entries), via the injected
	// persister so the backend-overwrite mechanism stays in the command layer.
	if fctx.PersistCatalog != nil && fctx.Cat != nil {
		removeFilesBySHA(fctx.Cat, doomSha)
		if err := fctx.PersistCatalog(ctx, fctx.Cat); err != nil {
			_ = fctx.Log.MarkFailed(ctx, rec.Entry.OpID, err.Error())
			return rep, fmt.Errorf("gc: persist catalog: %w", err)
		}
	}

	if err := fctx.Log.MarkDone(ctx, rec.Entry.OpID); err != nil {
		return rep, err
	}
	return rep, nil
}

// gitModeIndex returns the set of current shas of git-mode files and a sha→id
// map for them. ONLY sync_mode=="git" files are gc candidates (D14); manual and
// any unknown/future sync mode are excluded, making new modes safe by default
// (D15).
func gitModeIndex(cat *catalog.Catalog) (shas KeepSet, shaToID map[string]string) {
	shas = KeepSet{}
	shaToID = map[string]string{}
	if cat == nil {
		return
	}
	for _, f := range cat.Files {
		if f.SyncMode == "git" {
			shas.Add(f.SHA256)
			shaToID[f.SHA256] = f.ID
		}
	}
	return
}

// pendingIDSet collects the file ids locked by any pending (intent) WAL op — the
// D13 skip set. A nil log yields an empty set.
func pendingIDSet(ctx context.Context, log *wal.Log) (KeepSet, error) {
	ids := KeepSet{}
	if log == nil {
		return ids, nil
	}
	pend, err := log.Pending(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, r := range pend {
		for _, id := range r.Entry.BlobRefs {
			ids.Add(id)
		}
	}
	return ids, nil
}

// removeFilesBySHA drops every catalog file whose current sha is in shas.
func removeFilesBySHA(cat *catalog.Catalog, shas []string) {
	set := make(map[string]struct{}, len(shas))
	for _, s := range shas {
		set[s] = struct{}{}
	}
	var paths []string
	for _, f := range cat.Files {
		if _, ok := set[f.SHA256]; ok {
			paths = append(paths, f.Path)
		}
	}
	for _, p := range paths {
		cat.Remove(p)
	}
}
