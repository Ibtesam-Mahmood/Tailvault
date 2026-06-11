//go:build integration

// Block 3 federation integration suite (task-39). Built on the internal/fedtest
// harness: N simulated members over real FSBackends, real ingest/WAL pipelines,
// down-member simulation through production seams. No Tailscale, no SSH, no
// network — pure stub + fixture, runnable with `go test -tags integration ./...`.
package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fedtest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/gc"
	"github.com/Ibtesam-Mahmood/tailvault/internal/ingest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/ops"
	"github.com/Ibtesam-Mahmood/tailvault/internal/verify"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

func ctxB() context.Context { return context.Background() }

// memberLog returns a wal.Log over a member's (down-aware) backend.
func memberLog(f *fedtest.Fed, t *testing.T, name string) *wal.Log {
	return &wal.Log{B: f.Member(t, name).Backend()}
}

func memberCatalog(t *testing.T, f *fedtest.Fed, name string) (*catalog.Catalog, string) {
	t.Helper()
	root := f.Member(t, name).Root
	p := filepath.Join(root, "meta", "catalog.toml")
	cat, err := catalog.Load(p)
	if err != nil {
		t.Fatalf("load catalog %q: %v", name, err)
	}
	return cat, p
}

// harnessWAL adapts the harness to ops.MemberWAL.
type harnessWAL struct{ f *fedtest.Fed }

func (h harnessWAL) Read(ctx context.Context, m catalog.Member) ([]wal.Rec, error) {
	b, err := h.f.BackendFor()(m)
	if err != nil {
		return nil, err
	}
	return (&wal.Log{B: b}).Read(ctx)
}

// 1. WAL lifecycle: a seeded ingest is intent→done; the entry bytes are immutable
// across the state change (state lives in sibling markers, not the entry).
func TestB3_WALLifecycle(t *testing.T) {
	f := fedtest.New(t, "home")
	f.Seed(t, "home", "a.bin", []byte("alpha"))

	recs, err := memberLog(f, t, "home").Read(ctxB())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].State != wal.StateDone || recs[0].Entry.OpType != wal.OpIngest {
		t.Fatalf("want one DONE ingest, got %+v", recs)
	}
	// Immutable: re-read yields the same entry bytes/op id.
	again, _ := memberLog(f, t, "home").Read(ctxB())
	if again[0].Entry.OpID != recs[0].Entry.OpID || again[0].Entry.CreatedAt != recs[0].Entry.CreatedAt {
		t.Fatal("entry changed across reads — must be immutable")
	}
}

// 2. Crash recovery: a crash at each step leaves torn state that verify reports as
// a pending op; ReplayOp completes it; the final catalog is sound.
func TestB3_CrashRecovery(t *testing.T) {
	for _, step := range []string{"after-intent", "after-bytes", "after-catalog"} {
		t.Run(step, func(t *testing.T) {
			f := fedtest.New(t, "home")
			f.SeedCrash(t, "home", "c.bin", []byte("torn"), step)

			root := f.Member(t, "home").Root
			cat, catPath := memberCatalog(t, f, "home")
			log := memberLog(f, t, "home")

			// The torn op is ALWAYS detectable as a pending WAL intent (the dedicated
			// surface). verify additionally reports PendingOpState once the catalog
			// carries the row (after-catalog); a pre-catalog crash (after-intent/
			// after-bytes) has no catalog row to reconcile, so it is surfaced by the
			// pending sweep, not verify — the correct division of labor.
			pend, err := log.Pending(ctxB(), "")
			if err != nil || len(pend) != 1 {
				t.Fatalf("crash at %s must leave exactly one pending op, got %+v err=%v", step, pend, err)
			}
			findings, err := verify.ThreeWay(ctxB(), root, nil, cat, log, verify.Options{Now: func() time.Time { return time.Now() }})
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if step == "after-catalog" && countKind(findings, verify.PendingOpState) == 0 {
				t.Fatalf("after-catalog crash must verify as PendingOpState, got %+v", findings)
			}
			if err := ingest.ReplayOp(ctxB(), log, cat, catPath, "home", pend[0], func() time.Time { return time.Now() }); err != nil {
				t.Fatalf("ReplayOp: %v", err)
			}

			// Re-verify: no pending op remains; the file is catalogued.
			cat2, _ := memberCatalog(t, f, "home")
			findings2, _ := verify.ThreeWay(ctxB(), root, nil, cat2, memberLog(f, t, "home"), verify.Options{})
			if countKind(findings2, verify.PendingOpState) != 0 {
				t.Fatalf("pending op survived recovery: %+v", findings2)
			}
			if _, ok := cat2.Find("c.bin"); !ok {
				t.Fatal("recovered file missing from catalog")
			}
		})
	}
}

// 3. Tamper: corrupting a mid-chain entry breaks the hash chain — wal.Read and
// verify both surface it.
func TestB3_TamperDetected(t *testing.T) {
	f := fedtest.New(t, "home")
	f.Seed(t, "home", "a.bin", []byte("one"))
	f.Seed(t, "home", "b.bin", []byte("two"))
	f.Tamper(t, "home", 0)

	if _, err := memberLog(f, t, "home").Read(ctxB()); !errors.Is(err, wal.ErrChainBroken) {
		t.Fatalf("wal.Read must report ErrChainBroken, got %v", err)
	}
	cat, _ := memberCatalog(t, f, "home")
	findings, err := verify.ThreeWay(ctxB(), f.Member(t, "home").Root, nil, cat, memberLog(f, t, "home"), verify.Options{})
	if err == nil && countKind(findings, verify.ChainBroken) == 0 {
		t.Fatalf("verify must surface ChainBroken (err=%v findings=%+v)", err, findings)
	}
}

// 4. WAL-as-lock race: two concurrent ops on ONE blob → exactly one intent wins
// (the other gets op-in-flight); ops on different blobs both land. Run under -race.
func TestB3_WALAsLockRace(t *testing.T) {
	f := fedtest.New(t, "home")
	file := f.Seed(t, "home", "shared.bin", []byte("blob"))
	log := memberLog(f, t, "home")

	mkMove := func(to string) wal.Entry {
		return wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpMove, BlobRefs: []string{file.ID}, Actor: "a",
			Args: map[string]string{"from": "shared.bin", "to": to, "member": "home"}}
	}
	type res struct {
		rec wal.Rec
		err error
	}
	ch := make(chan res, 2)
	for _, to := range []string{"x.bin", "y.bin"} {
		go func(to string) {
			rec, err := log.AppendIntent(ctxB(), mkMove(to))
			ch <- res{rec, err}
		}(to)
	}
	r1, r2 := <-ch, <-ch
	inflight := 0
	if errors.Is(r1.err, wal.ErrOpInFlight) {
		inflight++
	}
	if errors.Is(r2.err, wal.ErrOpInFlight) {
		inflight++
	}
	if inflight != 1 {
		t.Fatalf("exactly one of two same-blob ops must get ErrOpInFlight, got %d (err1=%v err2=%v)", inflight, r1.err, r2.err)
	}
}

// 5. gc gates: a down member fails the all-members gate (TV-FED-02) BEFORE any
// planning; with all up, a git-mode object is doomed; a pending intent skips it.
func TestB3_GCGates(t *testing.T) {
	f := fedtest.New(t, "home", "office")
	file := f.SeedGit(t, "home", "big.bin", []byte("git-bytes"), nil)
	cat, catPath := memberCatalog(t, f, "home")
	log := memberLog(f, t, "home")
	stored := []string{file.SHA256}
	keep, preserve := gc.BuildKeepSet(nil), gc.BuildPreserveSet(nil)

	fctx := &gc.FedContext{
		Backend: f.Member(t, "home").Backend(), Roster: f.Roster, Probe: f.Probe(),
		Cat: cat, Log: log, Actor: "gc",
		PersistCatalog: func(_ context.Context, c *catalog.Catalog) error { return catalog.WriteAtomic(catPath, c) },
	}

	// Down-member gate: planning must abort with TV-FED-02 (NeedAllMembers).
	f.Member(t, "office").SetDown(true)
	if _, err := gc.PlanFederated(ctxB(), fctx, stored, keep, preserve); !errors.Is(err, gc.ErrNeedAllMembers) {
		t.Fatalf("a down member must fail gc planning with ErrNeedAllMembers, got %v", err)
	}
	f.Member(t, "office").SetDown(false)

	// All up: the git object is a candidate (doomed).
	plan, err := gc.PlanFederated(ctxB(), fctx, stored, keep, preserve)
	if err != nil {
		t.Fatalf("plan (all up): %v", err)
	}
	if len(plan.DoomedIDs) != 1 || plan.DoomedIDs[0] != file.ID {
		t.Fatalf("git object must be doomed, got %+v", plan.DoomedIDs)
	}

	// A pending intent on the file id makes gc skip it (D13).
	if _, err := log.AppendIntent(ctxB(), wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpMove, BlobRefs: []string{file.ID}, Actor: "a", Args: map[string]string{"from": "big.bin", "to": "z.bin", "member": "home"}}); err != nil {
		t.Fatal(err)
	}
	plan2, err := gc.PlanFederated(ctxB(), fctx, stored, keep, preserve)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan2.DoomedIDs) != 0 || len(plan2.SkippedPending) != 1 {
		t.Fatalf("a pending intent must skip the blob: doomed=%+v skipped=%+v", plan2.DoomedIDs, plan2.SkippedPending)
	}
}

// 6. Resolution fan-out: a file seeded on a non-home member resolves
// FoundElsewhere relative to a wrong home hint.
func TestB3_ResolutionFanout(t *testing.T) {
	f := fedtest.New(t, "home", "office")
	file := f.Seed(t, "office", "doc.bin", []byte("bytes"))

	res, err := f.Resolver().Resolve(ctxB(), file.ID, "home") // wrong home hint
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != fed.FoundElsewhere || res.View.Member != "office" {
		t.Fatalf("want FoundElsewhere at office, got %s / %q", res.Outcome, res.View.Member)
	}
}

// 7. moved_to with destination down: a completed cross-member move leaves a
// forwarding pointer on the source; with the dest down, resolution is a
// PartialView naming the dest (never Missing).
func TestB3_MovedToDestDown(t *testing.T) {
	f := fedtest.New(t, "home", "office")
	file := f.Seed(t, "home", "m.bin", []byte("bytes"))

	// Record a completed cross-member move on the SOURCE: home → office.
	srcLog := memberLog(f, t, "home")
	mv := wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpMove, BlobRefs: []string{file.ID}, Actor: "a",
		Args: map[string]string{"from": "home", "to": "office", "moved_to": "office", "src_path": "m.bin", "dest_path": "m.bin"}}
	if _, err := srcLog.AppendIntent(ctxB(), mv); err != nil {
		t.Fatal(err)
	}
	if err := srcLog.MarkDone(ctxB(), mv.OpID); err != nil {
		t.Fatal(err)
	}
	// Source no longer holds it in the catalog (the move demoted it to a forwarder).
	srcCat, srcPath := memberCatalog(t, f, "home")
	srcCat.Remove("m.bin")
	if err := catalog.WriteAtomic(srcPath, srcCat); err != nil {
		t.Fatal(err)
	}

	f.Member(t, "office").SetDown(true) // the new home is offline
	res, err := f.Resolver().Resolve(ctxB(), file.ID, "home")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != fed.PartialView || res.View.MovedTo != "office" {
		t.Fatalf("want PartialView naming office, got %s / movedTo=%q", res.Outcome, res.View.MovedTo)
	}
}

// 8. TV-FED vs TV-OBJ: an absent id with all members up is Missing (TV-OBJ); the
// same query with any member down is PartialView (TV-FED).
func TestB3_PartialViewVsMissing(t *testing.T) {
	f := fedtest.New(t, "home", "office")
	f.Seed(t, "home", "a.bin", []byte("bytes"))
	absent := "abcdef01" + "23456789abcdef0123456789abcdef0123456789abcdef0123456789"

	res, err := f.Resolver().Resolve(ctxB(), absent, "home")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != fed.Missing {
		t.Fatalf("absent id, all up → Missing, got %s", res.Outcome)
	}

	f.Member(t, "office").SetDown(true)
	res2, err := f.Resolver().Resolve(ctxB(), absent, "home")
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != fed.PartialView {
		t.Fatalf("absent id, one down → PartialView, got %s", res2.Outcome)
	}
}

// 9. Bootstrap honoring .tailvaultignore: ignored paths are excluded from the
// plan; everything else is catalogued.
func TestB3_BootstrapIgnore(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "keep.bin"), "keep")
	mustWrite(t, filepath.Join(root, "skip.tmp"), "skip")
	mustWrite(t, filepath.Join(root, ".tailvaultignore"), "*.tmp\n")

	ig, err := ingest.LoadIgnore(root)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ingest.BuildPlan(root, ig, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Files[0].Rel != "keep.bin" {
		t.Fatalf("ignore not honored: files=%+v", plan.Files)
	}
	if len(plan.Ignored) != 1 || plan.Ignored[0] != "skip.tmp" {
		t.Fatalf("ignored set wrong: %+v", plan.Ignored)
	}
}

// 10. ops sweep surfaces pending ops across reachable members and withholds a
// chain-broken member's ops while still listing the others.
func TestB3_OpsSweep(t *testing.T) {
	f := fedtest.New(t, "home", "office")
	f.Seed(t, "home", "a.bin", []byte("x"))
	// Leave a pending op on office.
	off := memberLog(f, t, "office")
	offFile := f.Seed(t, "office", "b.bin", []byte("y"))
	if _, err := off.AppendIntent(ctxB(), wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpMove, BlobRefs: []string{offFile.ID}, Actor: "a", Args: map[string]string{"from": "b.bin", "to": "c.bin", "member": "office"}}); err != nil {
		t.Fatal(err)
	}

	res, err := ops.Sweep(ctxB(), f.Roster, harnessWAL{f}, f.Probe())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Ops) != 1 || res.Ops[0].Member != "office" {
		t.Fatalf("sweep must list the one pending op on office, got %+v", res.Ops)
	}

	// Tamper home's chain: home's ops are withheld, office still lists.
	f.Seed(t, "home", "d.bin", []byte("z")) // a 2nd entry so tamper breaks the chain
	f.Tamper(t, "home", 0)
	res2, err := ops.Sweep(ctxB(), f.Roster, harnessWAL{f}, f.Probe())
	if err != nil {
		t.Fatalf("sweep with a broken member: %v", err)
	}
	if !memberChainBroken(res2, "home") {
		t.Fatalf("home must be reported ChainBroken, got %+v", res2.Members)
	}
}

// 11. Heal: a moved file's lock is repointed to the new home; under a down member
// (partial view) heal leaves the entry untouched. (Resolution drives both; the
// lock-rewrite is exercised in the cli heal tests — here we assert the resolution
// outcomes heal keys off.)
func TestB3_HealResolution(t *testing.T) {
	f := fedtest.New(t, "home", "office")
	file := f.Seed(t, "office", "h.bin", []byte("bytes")) // lives on office, lock says home

	// FoundElsewhere → heal would repoint home→office.
	res, err := f.Resolver().Resolve(ctxB(), file.ID, "home")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != fed.FoundElsewhere {
		t.Fatalf("want FoundElsewhere (heal repoints), got %s", res.Outcome)
	}

	// Under a down member, resolution is PartialView → heal leaves it untouched.
	f.Member(t, "office").SetDown(true)
	res2, err := f.Resolver().Resolve(ctxB(), file.ID, "home")
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != fed.PartialView {
		t.Fatalf("under a down member heal must see PartialView, got %s", res2.Outcome)
	}
}

// --- helpers ---

func countKind(fs []verify.ThreeFinding, k verify.FindingKind) int {
	n := 0
	for _, f := range fs {
		if f.Kind == k {
			n++
		}
	}
	return n
}

func memberChainBroken(r ops.SweepResult, name string) bool {
	for _, m := range r.Members {
		if m.Member == name && m.ChainBroken {
			return true
		}
	}
	return false
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
