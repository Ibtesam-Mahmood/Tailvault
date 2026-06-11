package gc

import (
	"bytes"
	"context"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
)

// seed plants blobs in the stub under objects/<sha> and returns the backend.
func seed(t *testing.T, shas ...string) *backend.FSBackend {
	t.Helper()
	b := backend.NewFSBackend(t.TempDir())
	for _, s := range shas {
		if err := b.Put(context.Background(), "objects/"+s, bytes.NewReader([]byte(s))); err != nil {
			t.Fatalf("seed %s: %v", s, err)
		}
	}
	return b
}

func TestBuildKeepSet_CrossBranchSurvival(t *testing.T) {
	// SHARED removed from branch main, still referenced by branch feature.
	branchLocks := map[string]*lock.Lock{
		"main":    {Entries: []lock.Entry{{Path: "a", SHA256: "A"}}},
		"feature": {Entries: []lock.Entry{{Path: "a", SHA256: "A"}, {Path: "s", SHA256: "SHARED"}}},
		"empty":   nil, // a branch with no committed lock contributes nothing
	}
	keep := BuildKeepSet(branchLocks)
	if !keep.Has("A") || !keep.Has("SHARED") {
		t.Fatalf("keep-set missing union members: %v", keep)
	}
	plan := PlanSweep([]string{"objects/A", "objects/SHARED", "objects/GONE"}, keep, KeepSet{})
	if len(plan.Eligible) != 1 || plan.Eligible[0] != "GONE" {
		t.Errorf("Eligible = %v, want [GONE] (SHARED kept by feature branch)", plan.Eligible)
	}
}

func TestBuildKeepSet_HistoryVersionsKept(t *testing.T) {
	branchLocks := map[string]*lock.Lock{
		"main": {Entries: []lock.Entry{
			{Path: "h", SHA256: "v3", History: true, Versions: []string{"v3", "v2", "v1"}},
		}},
	}
	keep := BuildKeepSet(branchLocks)
	plan := PlanSweep([]string{"objects/v3", "objects/v2", "objects/v1"}, keep, KeepSet{})
	if len(plan.Eligible) != 0 {
		t.Errorf("history versions should all be kept, got eligible %v", plan.Eligible)
	}
	if plan.Kept != 3 {
		t.Errorf("Kept = %d, want 3", plan.Kept)
	}
}

func TestBuildPreserveSet(t *testing.T) {
	branchLocks := map[string]*lock.Lock{
		"main": {Entries: []lock.Entry{
			{Path: "p", SHA256: "P", Preserve: true, History: true, Versions: []string{"P", "Pold"}},
			{Path: "n", SHA256: "N"}, // not preserve
		}},
	}
	pres := BuildPreserveSet(branchLocks)
	if !pres.Has("P") || !pres.Has("Pold") {
		t.Errorf("preserve-set should include P and Pold: %v", pres)
	}
	if pres.Has("N") {
		t.Error("preserve-set must not include non-preserve sha N")
	}
}

func TestPreserveSurvivesSweep(t *testing.T) {
	// P was deleted from every lock (not in keep), but preserve protects it.
	keep := KeepSet{}
	keep.Add("A")
	pres := KeepSet{}
	pres.Add("P")
	plan := PlanSweep([]string{"objects/A", "objects/P", "objects/GONE"}, keep, pres)
	if len(plan.Eligible) != 1 || plan.Eligible[0] != "GONE" {
		t.Errorf("Eligible = %v, want [GONE] (P preserved)", plan.Eligible)
	}
	if plan.Preserved != 1 {
		t.Errorf("Preserved = %d, want 1", plan.Preserved)
	}
}

func TestSweep_DeletesEligible(t *testing.T) {
	ctx := context.Background()
	b := seed(t, "A", "GONE")
	plan := Plan{Eligible: []string{"GONE"}}

	// dry-run deletes nothing.
	if n, err := Sweep(ctx, b, plan, true); err != nil || n != 0 {
		t.Fatalf("dry-run Sweep = %d, %v; want 0, nil", n, err)
	}
	if b.Deletes != 0 {
		t.Errorf("dry-run performed %d Deletes, want 0", b.Deletes)
	}
	if m, _ := b.Stat(ctx, "objects/GONE"); !m.Exists {
		t.Error("dry-run must not remove the blob")
	}

	// real sweep deletes exactly the eligible blob.
	n, err := Sweep(ctx, b, plan, false)
	if err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v; want 1, nil", n, err)
	}
	if b.Deletes != 1 {
		t.Errorf("Deletes counter = %d, want 1", b.Deletes)
	}
	if m, _ := b.Stat(ctx, "objects/GONE"); m.Exists {
		t.Error("GONE should be deleted")
	}
	if m, _ := b.Stat(ctx, "objects/A"); !m.Exists {
		t.Error("A must survive (not eligible)")
	}
}
