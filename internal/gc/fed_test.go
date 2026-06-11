package gc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

var bg = context.Background()

func mkMember(name string) catalog.Member {
	return catalog.Member{Name: name, Node: name + ".ts.net", JoinedAt: time.Unix(0, 0).UTC(), Status: "active"}
}

func mkRoster(members ...catalog.Member) fed.Roster {
	return fed.Roster{FedID: "fed-1", Members: members}
}

func mkProber(down ...string) func(context.Context, catalog.Member) error {
	set := map[string]bool{}
	for _, d := range down {
		set[d] = true
	}
	return func(_ context.Context, m catalog.Member) error {
		if set[m.Name] {
			return errors.New("down")
		}
		return nil
	}
}

func mkFile(id, sha, path, mode string) catalog.File {
	return catalog.File{ID: id, SHA256: sha, Path: path, SyncMode: mode, Size: 1}
}

func mkCatalog(files ...catalog.File) *catalog.Catalog {
	return &catalog.Catalog{Version: 2, VaultName: "v", Node: "n", Federation: catalog.Federation{FedID: "fed-1"}, Files: files}
}

func mkFS(t *testing.T) *backend.FSBackend { t.Helper(); return backend.NewFSBackend(t.TempDir()) }

// appendMove writes a move WAL op locking id; done marks it complete.
func appendMove(t *testing.T, log *wal.Log, id string, done bool) {
	t.Helper()
	rec, err := log.AppendIntent(bg, wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpMove, BlobRefs: []string{id}, Actor: "t"})
	if err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if done {
		if err := log.MarkDone(bg, rec.Entry.OpID); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}
	}
}

func TestPlanFederated_AllMembersGate(t *testing.T) {
	fctx := &FedContext{
		Roster: mkRoster(mkMember("pi-1"), mkMember("pi-2")),
		Probe:  mkProber("pi-2"), // pi-2 down
		Cat:    mkCatalog(mkFile("idG", "shaG", "g.bin", "git")),
	}
	_, err := PlanFederated(bg, fctx, []string{"objects/shaG"}, KeepSet{}, KeepSet{})
	if !errors.Is(err, ErrNeedAllMembers) {
		t.Fatalf("down member must fail the all-members gate; got %v", err)
	}
}

func TestPlanFederated_GitOnlyScope(t *testing.T) {
	c := mkCatalog(
		mkFile("idG", "shaG", "g.bin", "git"),    // git + unreferenced → eligible
		mkFile("idM", "shaM", "m.bin", "manual"), // manual → never collectable
		mkFile("idS", "shaS", "s.bin", "s3"),     // unknown future mode → never collectable
	)
	fctx := &FedContext{Roster: mkRoster(mkMember("pi-1")), Probe: mkProber(), Cat: c}
	// shaX has no catalog entry at all → excluded (not a git-mode object).
	stored := []string{"objects/shaG", "objects/shaM", "objects/shaS", "objects/shaX"}

	p, err := PlanFederated(bg, fctx, stored, KeepSet{}, KeepSet{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Eligible) != 1 || p.Eligible[0] != "shaG" {
		t.Errorf("only the unreferenced git object is eligible; got %v", p.Eligible)
	}
	if len(p.DoomedIDs) != 1 || p.DoomedIDs[0] != "idG" {
		t.Errorf("DoomedIDs = %v, want [idG]", p.DoomedIDs)
	}
}

func TestPlanFederated_KeepSetProtectsGit(t *testing.T) {
	c := mkCatalog(mkFile("idG", "shaG", "g.bin", "git"))
	keep := KeepSet{}
	keep.Add("shaG") // referenced by a branch lock
	fctx := &FedContext{Roster: mkRoster(mkMember("pi-1")), Probe: mkProber(), Cat: c}

	p, err := PlanFederated(bg, fctx, []string{"objects/shaG"}, keep, KeepSet{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Eligible) != 0 || p.Kept != 1 {
		t.Errorf("a referenced git object must be kept; got eligible=%v kept=%d", p.Eligible, p.Kept)
	}
}

func TestPlanFederated_PendingSkip(t *testing.T) {
	fsb := mkFS(t)
	log := &wal.Log{B: fsb}
	appendMove(t, log, "idG", false) // in-flight move locks idG

	c := mkCatalog(mkFile("idG", "shaG", "g.bin", "git"))
	fctx := &FedContext{Roster: mkRoster(mkMember("pi-1")), Probe: mkProber(), Cat: c, Log: log}

	p, err := PlanFederated(bg, fctx, []string{"objects/shaG"}, KeepSet{}, KeepSet{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Eligible) != 0 {
		t.Errorf("a blob with a pending intent must be skipped; eligible=%v", p.Eligible)
	}
	if len(p.SkippedPending) != 1 || p.SkippedPending[0] != "shaG" {
		t.Errorf("SkippedPending = %v, want [shaG]", p.SkippedPending)
	}
}

func TestSweepFederated_JournaledWithCatalogUpdate(t *testing.T) {
	fsb := mkFS(t)
	log := &wal.Log{B: fsb}
	if err := fsb.Put(bg, "objects/shaG", strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	c := mkCatalog(mkFile("idG", "shaG", "g.bin", "git"))
	persisted := 0
	fctx := &FedContext{
		Backend: fsb, Roster: mkRoster(mkMember("pi-1")), Probe: mkProber(), Cat: c, Log: log, Actor: "tester",
		PersistCatalog: func(_ context.Context, _ *catalog.Catalog) error { persisted++; return nil },
	}

	p, err := PlanFederated(bg, fctx, []string{"objects/shaG"}, KeepSet{}, KeepSet{})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := SweepFederated(bg, fctx, p, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", rep.Deleted)
	}
	if m, _ := fsb.Stat(bg, "objects/shaG"); m.Exists {
		t.Error("object should be deleted")
	}
	if persisted != 1 {
		t.Errorf("PersistCatalog calls = %d, want 1", persisted)
	}
	if _, ok := c.Find("g.bin"); ok {
		t.Error("doomed file should be removed from the catalog")
	}
	// The sweep must be journaled as a DONE gc op.
	recs, err := log.Read(bg)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range recs {
		if r.Entry.OpType == wal.OpGC {
			found = true
			if r.State != wal.StateDone {
				t.Errorf("gc op state = %s, want done", r.State)
			}
			if len(r.Entry.BlobRefs) != 1 || r.Entry.BlobRefs[0] != "idG" {
				t.Errorf("gc op blob_refs = %v, want [idG]", r.Entry.BlobRefs)
			}
		}
	}
	if !found {
		t.Error("sweep must journal a gc WAL op")
	}
}

func TestSweepFederated_ReskipOnRace(t *testing.T) {
	fsb := mkFS(t)
	log := &wal.Log{B: fsb}
	_ = fsb.Put(bg, "objects/shaG", strings.NewReader("data"))
	c := mkCatalog(mkFile("idG", "shaG", "g.bin", "git"))
	fctx := &FedContext{Backend: fsb, Roster: mkRoster(mkMember("pi-1")), Probe: mkProber(), Cat: c, Log: log, Actor: "t"}

	// Plan with no pending → shaG eligible.
	p, err := PlanFederated(bg, fctx, []string{"objects/shaG"}, KeepSet{}, KeepSet{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Eligible) != 1 {
		t.Fatalf("expected shaG eligible, got %v", p.Eligible)
	}
	// A move intent appears between Plan and Sweep.
	appendMove(t, log, "idG", false)

	rep, err := SweepFederated(bg, fctx, p, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Deleted != 0 {
		t.Errorf("a blob locked between plan and sweep must NOT be deleted; Deleted=%d", rep.Deleted)
	}
	if len(rep.SkippedPending) != 1 {
		t.Errorf("the raced blob must be re-skipped; SkippedPending=%v", rep.SkippedPending)
	}
	if m, _ := fsb.Stat(bg, "objects/shaG"); !m.Exists {
		t.Error("object must survive the raced sweep")
	}
}

func TestSweepFederated_DryRunMutatesNothing(t *testing.T) {
	fsb := mkFS(t)
	log := &wal.Log{B: fsb}
	_ = fsb.Put(bg, "objects/shaG", strings.NewReader("data"))
	c := mkCatalog(mkFile("idG", "shaG", "g.bin", "git"))
	fctx := &FedContext{Backend: fsb, Roster: mkRoster(mkMember("pi-1")), Probe: mkProber(), Cat: c, Log: log, Actor: "t"}

	p, _ := PlanFederated(bg, fctx, []string{"objects/shaG"}, KeepSet{}, KeepSet{})
	rep, err := SweepFederated(bg, fctx, p, true /* dryRun */)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Deleted != 0 {
		t.Errorf("dry-run deleted %d", rep.Deleted)
	}
	if m, _ := fsb.Stat(bg, "objects/shaG"); !m.Exists {
		t.Error("dry-run must not delete the object")
	}
	recs, _ := log.Read(bg)
	for _, r := range recs {
		if r.Entry.OpType == wal.OpGC {
			t.Error("dry-run must not journal a gc op")
		}
	}
}

func TestPlanFederated_NilContextGuard(t *testing.T) {
	if _, err := PlanFederated(bg, nil, nil, KeepSet{}, KeepSet{}); err == nil {
		t.Error("PlanFederated(nil) should error (non-federated vaults use PlanSweep)")
	}
	if _, err := SweepFederated(bg, nil, PlanResult{Plan: Plan{Eligible: []string{"x"}}}, false); err == nil {
		t.Error("SweepFederated(nil) should error")
	}
}
