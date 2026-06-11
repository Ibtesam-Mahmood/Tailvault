package cli

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fedtest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// Block-4 suite — Row 5: reachability / crash / restore (task-50). Every case
// drives the REAL root command via execCLI and asserts the BUCKETED process exit
// code (7b) + the SPECIFIC tserr.Code (non-vacuity) + on-disk state. The
// down-member seam (cli.SetTestBackendFor → fedtest.MemberBackend) makes a
// CLI-resolved command honor harness SetDown for BOTH data and reachability.

// installSeam wires the harness's down-aware backends into the cli backend seam
// so a command resolving a member sees SetDown. Cleaned up at test end.
func installSeam(t *testing.T, f *fedtest.Fed) {
	t.Helper()
	SetTestBackendFor(func(name string) (backend.Backend, bool) {
		if b := f.MemberBackend(name); b != nil {
			return b, true
		}
		return nil, false
	})
	t.Cleanup(func() { SetTestBackendFor(nil) })
}

// memberCatalog loads a member's on-disk catalog (fatal on error).
func memberCatalog(t *testing.T, f *fedtest.Fed, member string) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.Load(filepath.Join(f.Member(t, member).Root, "meta", "catalog.toml"))
	if err != nil {
		t.Fatalf("load %s catalog: %v", member, err)
	}
	return cat
}

// writeRestoreReceipt writes a pull-receipt TOML carrying id+genesis and returns
// its path — the restore source for an identity-restoration command.
func writeRestoreReceipt(t *testing.T, id string, g identity.Genesis) string {
	t.Helper()
	dir := t.TempDir()
	if err := identity.WriteReceipt(dir, identity.Receipt{
		ID: id, Genesis: g, Path: g.OriginalPath, SHA256AtPull: g.ContentSHA256,
		PulledAt: time.Unix(1700000000, 0).UTC(), SourceNode: g.OriginNode,
	}); err != nil {
		t.Fatalf("write receipt: %v", err)
	}
	return filepath.Join(dir, id+".toml")
}

// TestBlock4_StatDownMemberIsPartialViewNotMissing (Row 5 case 1): an id whose
// sole holder is unreachable must NOT be reported as a clean miss — absence
// cannot be proven, so it is TV-FED (exit 6), never TV-OBJ (exit 5). The contrast
// case (all members up, genuinely-unknown id) IS a real miss → exit 5.
func TestBlock4_StatDownMemberIsPartialViewNotMissing(t *testing.T) {
	// FED-LOOKUP-1 FIXED (coder-c, vault_common.go fileByIDPrefix): an id-prefix
	// lookup that finds 0 matches while an active member was unreachable now returns
	// TV-FED-01 (PartialView, exit 6) instead of a silent TV-OBJ-01 miss — so
	// stat/get/mv/rm by id honor the exit-6-vs-5 discipline. This case is now live.

	f := fedtest.New(t, "home", "office")
	installSeam(t, f)
	held := f.Seed(t, "office", "media/clip.mp4", []byte("on office\n"))

	// Contrast: all up + an id no member holds → genuine miss (exit 5, TV-OBJ).
	missingID := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	_, code, err := execCLI("vault", "stat", missingID)
	if code != exitObj {
		t.Fatalf("genuinely-missing id (all up): exit %d, want %d (TV-OBJ)\nerr=%v", code, exitObj, err)
	}
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.ObjMissing {
		t.Fatalf("genuine miss must be TV-OBJ-01, got %v", err)
	}

	// office (sole holder of held.ID) goes down → absence can't be proven → exit 6.
	f.Member(t, "office").SetDown(true)
	_, code, err = execCLI("vault", "stat", held.ID)
	if code != exitFed {
		t.Fatalf("down sole-holder must be TV-FED (exit 6), got exit %d\nerr=%v", code, err)
	}
	if !errors.As(err, &te) || te.Code != tserr.FedPartialView {
		t.Fatalf("down sole-holder must be TV-FED-01 (PartialView), got %v", err)
	}
}

// TestBlock4_CrashIngestRetriesOnce (Row 5 case 2): a crash mid-ingest leaves a
// pending WAL intent; `ops retry <op>` completes it through the SAME engine code
// (ingest.ReplayOp) — exactly one done op, no duplicate, file in the catalog.
func TestBlock4_CrashIngestRetriesOnce(t *testing.T) {
	f := fedtest.New(t, "home")
	opID := f.SeedCrash(t, "home", "media/x.bin", []byte("after-bytes content\n"), "after-bytes")

	// Pre-retry: the op is pending (not done) and the file is not yet in the catalog.
	home := f.Member(t, "home").Root
	if _, ok := memberCatalog(t, f, "home").Find("media/x.bin"); ok {
		t.Fatal("a crashed-after-bytes ingest must NOT yet be in the catalog")
	}

	out, code, err := execCLI("ops", "retry", opID)
	if code != exitOK || err != nil {
		t.Fatalf("ops retry: exit %d / %v\n%s", code, err, out)
	}

	// Exactly one ingest op, now done; no duplicate intent appended.
	recs, rerr := (&wal.Log{B: backend.NewTaildrive(home)}).Read(context.Background())
	if rerr != nil {
		t.Fatalf("read wal: %v", rerr)
	}
	ingestDone := 0
	for _, r := range recs {
		if r.Entry.OpType == wal.OpIngest {
			if r.Entry.OpID != opID {
				t.Errorf("a retry must not mint a new ingest op: saw %s (want only %s)", r.Entry.OpID, opID)
			}
			if r.State == wal.StateDone {
				ingestDone++
			}
		}
	}
	if ingestDone != 1 {
		t.Errorf("want exactly 1 done ingest op, got %d (recs=%d)", ingestDone, len(recs))
	}
	if _, ok := memberCatalog(t, f, "home").Find("media/x.bin"); !ok {
		t.Error("after retry the file must be in the catalog")
	}
}

// TestBlock4_RestoreIdentityCollisionIsFed04 (Row 5 case 3a): restoring an id
// that is LIVE on a reachable member is a federation-wide collision — refuse
// TV-FED-04 (exit 6), local catalog byte-untouched. (restore-identity is gated;
// taildrive gating is a node-side no-op, DEV-46.8, so no password is needed.)
func TestBlock4_RestoreIdentityCollisionIsFed04(t *testing.T) {
	f := fedtest.New(t, "home", "office")
	installSeam(t, f)
	// office holds the original identity live.
	held := f.Seed(t, "office", "media/clip.mp4", []byte("the original\n"))
	// home has a rebuilt entry at a path with a DIFFERENT (re-minted) id.
	local := f.Seed(t, "home", "doc/clip.mp4", []byte("rebuilt copy\n"))
	if local.ID == held.ID {
		t.Fatal("fixture: home and office ids must differ")
	}
	receipt := writeRestoreReceipt(t, held.ID, identity.Genesis(held.Genesis))

	before := memberCatalog(t, f, "home")
	_, code, err := execCLI("vault", "restore-identity", "home/doc/clip.mp4", "--receipt", receipt, "--yes")
	if code != exitFed {
		t.Fatalf("collision on a reachable member must be TV-FED (exit 6), got exit %d\nerr=%v", code, err)
	}
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.FedIDCollision {
		t.Fatalf("reachable-member collision must be TV-FED-04, got %v", err)
	}
	// Local catalog untouched: home/doc/clip.mp4 still carries its re-minted id.
	after, _ := memberCatalog(t, f, "home").Find("doc/clip.mp4")
	if after.ID != local.ID {
		t.Errorf("catalog must be untouched on a collision: id %s, want %s", after.ID, local.ID)
	}
	if b, _ := before.Find("doc/clip.mp4"); b.ID != after.ID {
		t.Error("catalog mutated despite the refusal")
	}
}

// TestBlock4_RestoreIdentityPartialViewRefuses (Row 5 case 3b): with a member
// unreachable, a restore whose id is not found among the reachable members cannot
// prove no-collision → refuse TV-FED-01 (PartialView, exit 6), catalog untouched.
func TestBlock4_RestoreIdentityPartialViewRefuses(t *testing.T) {
	f := fedtest.New(t, "home", "office")
	installSeam(t, f)
	local := f.Seed(t, "home", "media/clip.mp4", []byte("rebuilt\n"))

	// A standalone genesis no reachable member holds.
	g := identity.Genesis{
		ContentSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		OriginalPath:  "media/clip.mp4", IngestOpID: "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6", OriginNode: "home",
	}
	origID, _ := identity.MintID(g)
	if origID == local.ID {
		t.Fatal("fixture: restore id must differ from the live entry")
	}
	receipt := writeRestoreReceipt(t, origID, g)

	f.Member(t, "office").SetDown(true) // can't prove origID is absent federation-wide
	_, code, err := execCLI("vault", "restore-identity", "home/media/clip.mp4", "--receipt", receipt, "--yes")
	if code != exitFed {
		t.Fatalf("restore under a partial view must be TV-FED (exit 6), got exit %d\nerr=%v", code, err)
	}
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.FedPartialView {
		t.Fatalf("unreachable-member restore must be TV-FED-01 (PartialView), got %v", err)
	}
	if after, _ := memberCatalog(t, f, "home").Find("media/clip.mp4"); after.ID != local.ID {
		t.Errorf("catalog must be untouched under a partial view: id %s, want %s", after.ID, local.ID)
	}
}
