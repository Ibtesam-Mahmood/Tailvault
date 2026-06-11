package ingest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

func restoreEnv(t *testing.T) (*backend.FSBackend, RestoreOpts) {
	t.Helper()
	be := backend.NewFSBackend(t.TempDir())
	return be, RestoreOpts{
		Backend: be, Log: &wal.Log{B: be},
		Cat:  &catalog.Catalog{Version: catalog.SchemaVersion, Node: testNode},
		Node: testNode, Actor: "tester",
		Now: func() time.Time { return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC) },
	}
}

// origGenesis is a surviving genesis record (from a receipt/lock); its id is the
// ORIGINAL identity to restore.
func origGenesis() (identity.Genesis, string) {
	g := identity.Genesis{
		ContentSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		OriginalPath:  "media/clip.mp4", IngestOpID: "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6", OriginNode: "home-pi",
	}
	id, _ := identity.MintID(g)
	return g, id
}

func TestRestoreIdentitySwapsToOriginal(t *testing.T) {
	ctx := context.Background()
	be, o := restoreEnv(t)
	g, origID := origGenesis()

	// Rebuilt catalog: the file is back but carries a RE-MINTED id.
	o.Cat.Upsert(catalog.File{
		ID: "reminted000000000000000000000000000000000000000000000000000000", SHA256: "ff",
		Path: "media/clip.mp4", SyncMode: catalog.SyncModeManual,
	})

	f, err := RestoreIdentity(ctx, o, "media/clip.mp4", origID, g)
	if err != nil {
		t.Fatalf("RestoreIdentity: %v", err)
	}
	if f.ID != origID {
		t.Fatalf("id not restored: %s want %s", f.ID, origID)
	}
	if f.SHA256 != "ff" || f.SyncMode != catalog.SyncModeManual {
		t.Errorf("current content/mode not preserved: %+v", f)
	}
	// WAL restore op recorded + done; catalog persisted over backend.
	recs, _ := o.Log.Read(ctx)
	if len(recs) != 1 || recs[0].Entry.OpType != wal.OpRestore || recs[0].State != wal.StateDone {
		t.Fatalf("wal recs = %+v", recs)
	}
	if recs[0].Entry.Args["restored_id"] != origID {
		t.Errorf("restore op args missing restored_id: %+v", recs[0].Entry.Args)
	}
	persisted, err := catalog.Load(catPathOf(be))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := persisted.Find("media/clip.mp4"); got.ID != origID {
		t.Errorf("persisted catalog id = %s, want %s", got.ID, origID)
	}
}

// TestRestoreIdentityOpIsProjectionSufficient is fix-35-B: the OpRestore WAL
// entry must carry the full genesis PREIMAGE (not just restored_id = sha256(g)),
// so a catalog rebuild from the WAL alone (ProjectCatalog) can reconstruct
// f.Genesis. This asserts the writer half — the args reconstruct g exactly and g
// self-certifies to restored_id. The end-to-end ProjectCatalog round-trip rides
// with coder-b's applyOp OpRestore case (fix-35-A / #39).
func TestRestoreIdentityOpIsProjectionSufficient(t *testing.T) {
	ctx := context.Background()
	_, o := restoreEnv(t)
	g, origID := origGenesis()
	o.Cat.Upsert(catalog.File{
		ID: "reminted000000000000000000000000000000000000000000000000000000", SHA256: "ff",
		Path: "media/clip.mp4", SyncMode: catalog.SyncModeManual,
	})
	if _, err := RestoreIdentity(ctx, o, "media/clip.mp4", origID, g); err != nil {
		t.Fatalf("RestoreIdentity: %v", err)
	}
	recs, _ := o.Log.Read(ctx)
	args := recs[0].Entry.Args

	rebuilt := identity.Genesis{
		ContentSHA256: args["content_sha256"],
		OriginalPath:  args["original_path"],
		IngestOpID:    args["ingest_op_id"],
		OriginNode:    args["origin_node"],
	}
	if rebuilt != g {
		t.Fatalf("genesis preimage not faithfully recorded in op args:\n got  %+v\n want %+v\n args %+v", rebuilt, g, args)
	}
	// projection-sufficiency: the recorded preimage must hash back to restored_id.
	ok, err := identity.Verify(rebuilt, args["restored_id"])
	if err != nil || !ok {
		t.Fatalf("reconstructed genesis must self-certify restored_id: ok=%v err=%v", ok, err)
	}
	if args["restored_id"] != origID {
		t.Errorf("restored_id = %s, want %s", args["restored_id"], origID)
	}
}

func TestRestoreIdentityNoTarget(t *testing.T) {
	ctx := context.Background()
	_, o := restoreEnv(t)
	g, origID := origGenesis()
	if _, err := RestoreIdentity(ctx, o, "missing/path", origID, g); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("want ErrNoTarget, got %v", err)
	}
}

func TestRestoreIdentityAlreadyRestored(t *testing.T) {
	ctx := context.Background()
	_, o := restoreEnv(t)
	g, origID := origGenesis()
	o.Cat.Upsert(catalog.File{ID: origID, SHA256: "ff", Path: "media/clip.mp4", SyncMode: catalog.SyncModeManual})
	if _, err := RestoreIdentity(ctx, o, "media/clip.mp4", origID, g); !errors.Is(err, ErrAlreadyRestored) {
		t.Fatalf("want ErrAlreadyRestored, got %v", err)
	}
}

func TestRestoreIdentityCollision(t *testing.T) {
	ctx := context.Background()
	_, o := restoreEnv(t)
	g, origID := origGenesis()
	// originalID already live on a DIFFERENT path → restoring would double-claim.
	o.Cat.Upsert(catalog.File{ID: origID, SHA256: "aa", Path: "other/file", SyncMode: catalog.SyncModeManual})
	o.Cat.Upsert(catalog.File{ID: "reminted000000000000000000000000000000000000000000000000000000", SHA256: "ff",
		Path: "media/clip.mp4", SyncMode: catalog.SyncModeManual})
	if _, err := RestoreIdentity(ctx, o, "media/clip.mp4", origID, g); !errors.Is(err, ErrIDCollision) {
		t.Fatalf("want ErrIDCollision, got %v", err)
	}
}

func TestRestoreIdentityRejectsNonCertifying(t *testing.T) {
	ctx := context.Background()
	_, o := restoreEnv(t)
	g, origID := origGenesis()
	o.Cat.Upsert(catalog.File{ID: "x000000000000000000000000000000000000000000000000000000000000", SHA256: "ff",
		Path: "media/clip.mp4", SyncMode: catalog.SyncModeManual})
	// Tamper the record so it no longer hashes to origID.
	g.OriginNode = "tampered"
	if _, err := RestoreIdentity(ctx, o, "media/clip.mp4", origID, g); err == nil {
		t.Fatal("a record that does not self-certify must be rejected")
	}
}
