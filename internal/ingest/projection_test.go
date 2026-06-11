package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// hexSha gives a real 64-hex digest from a seed (identity requires 64 hex).
func hexSha(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// ingestEntry builds a deterministic ingest WAL entry + its minted id.
func ingestEntry(t *testing.T, path, seed string) (wal.Entry, string) {
	t.Helper()
	content := hexSha(seed)
	g := identity.Genesis{ContentSHA256: content, OriginalPath: path, IngestOpID: ingestOpID(path), OriginNode: testNode}
	id, err := identity.MintID(g)
	if err != nil {
		t.Fatal(err)
	}
	return wal.Entry{
		OpID: g.IngestOpID, OpType: wal.OpIngest, BlobRefs: []string{id}, Actor: "a", CreatedAt: replayClock()(),
		Args: map[string]string{
			"path": path, "content_sha256": content, "origin_node": testNode,
			"sync_mode": catalog.SyncModeManual, "size": "5",
		},
	}, id
}

// TestProjectCatalogEqualsForwardReplay is the load-bearing invariant: a catalog
// rebuilt purely from the DONE WAL (ProjectCatalog) is byte-identical to the
// catalog produced by applying those same ops forward (ReplayOp). The projection
// can never diverge from how ops were originally applied — same applyOp core.
func TestProjectCatalogEqualsForwardReplay(t *testing.T) {
	ctx := context.Background()
	log, live, catPath := replayEnv(t)

	ingA, _ := ingestEntry(t, "a.txt", "aaa")
	ingB, idB := ingestEntry(t, "b.txt", "bbb")
	for _, e := range []wal.Entry{ingA, ingB} {
		rec, err := log.AppendIntent(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		if err := ReplayOp(ctx, log, live, catPath, testNode, rec, replayClock()); err != nil {
			t.Fatalf("forward replay ingest: %v", err)
		}
	}
	// scan-edit a.txt, then move b.txt -> sub/b.txt (id preserved).
	scanRec, err := log.AppendIntent(ctx, wal.Entry{
		OpID: wal.NewOpID(), OpType: wal.OpScan, BlobRefs: []string{"a"}, Actor: "a", CreatedAt: replayClock()(),
		Args: map[string]string{"path": "a.txt", "old_sha256": "aaa", "new_sha256": "aaa2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ReplayOp(ctx, log, live, catPath, testNode, scanRec, replayClock()); err != nil {
		t.Fatal(err)
	}
	moveRec, err := log.AppendIntent(ctx, wal.Entry{
		OpID: wal.NewOpID(), OpType: wal.OpMove, BlobRefs: []string{idB}, Actor: "a", CreatedAt: replayClock()(),
		Args: map[string]string{"from": "b.txt", "to": "sub/b.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ReplayOp(ctx, log, live, catPath, testNode, moveRec, replayClock()); err != nil {
		t.Fatal(err)
	}

	recs, err := log.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := &catalog.Catalog{Version: catalog.SchemaVersion, Node: testNode}
	rebuilt, err := ProjectCatalog(base, recs, testNode)
	if err != nil {
		t.Fatalf("ProjectCatalog: %v", err)
	}

	live.Canonicalize()
	liveBytes, err := catalog.Encode(live)
	if err != nil {
		t.Fatal(err)
	}
	rebuiltBytes, err := catalog.Encode(rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(liveBytes, rebuiltBytes) {
		t.Fatalf("projection != forward replay\n--- live ---\n%s\n--- rebuilt ---\n%s", liveBytes, rebuiltBytes)
	}
	// spot-check the reconstructed content (not just byte-equality).
	if f, ok := rebuilt.Find("a.txt"); !ok || f.SHA256 != "aaa2" {
		t.Errorf("a.txt not reconstructed with edited sha: %+v ok=%v", f, ok)
	}
	if f, ok := rebuilt.Find("sub/b.txt"); !ok || f.ID != idB {
		t.Errorf("b.txt move not reconstructed with preserved id: %+v ok=%v", f, ok)
	}
}

// TestProjectCatalogSkipsIntentAndFailed: only DONE ops contribute. A pending
// intent (crashed before completion) must NOT appear in a rebuild.
func TestProjectCatalogSkipsIntentAndFailed(t *testing.T) {
	ctx := context.Background()
	log, _, _ := replayEnv(t)

	ingDone, _ := ingestEntry(t, "done.txt", "ddd")
	doneRec, err := log.AppendIntent(ctx, ingDone)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.MarkDone(ctx, doneRec.Entry.OpID); err != nil {
		t.Fatal(err)
	}
	// a second op left pending (intent), and a third explicitly failed.
	ingPending, _ := ingestEntry(t, "pending.txt", "ppp")
	if _, err := log.AppendIntent(ctx, ingPending); err != nil {
		t.Fatal(err)
	}
	ingFailed, _ := ingestEntry(t, "failed.txt", "fff")
	failRec, err := log.AppendIntent(ctx, ingFailed)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.MarkFailed(ctx, failRec.Entry.OpID, "disk full"); err != nil {
		t.Fatal(err)
	}

	recs, err := log.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := ProjectCatalog(&catalog.Catalog{Version: catalog.SchemaVersion, Node: testNode}, recs, testNode)
	if err != nil {
		t.Fatalf("ProjectCatalog: %v", err)
	}
	if len(rebuilt.Files) != 1 {
		t.Fatalf("only the DONE op should project, got %d files: %+v", len(rebuilt.Files), rebuilt.Files)
	}
	if _, ok := rebuilt.Find("done.txt"); !ok {
		t.Error("the DONE ingest is missing from the rebuild")
	}
	if _, ok := rebuilt.Find("pending.txt"); ok {
		t.Error("a pending (intent) op must NOT appear in the rebuild")
	}
	if _, ok := rebuilt.Find("failed.txt"); ok {
		t.Error("a failed op must NOT appear in the rebuild")
	}
}

// TestProjectCatalogDeterministic: two rebuilds of the same WAL produce
// byte-identical catalogs (timestamps from immutable CreatedAt, sorted on
// Canonicalize) — the SG-6 recovery is reproducible.
func TestProjectCatalogDeterministic(t *testing.T) {
	ctx := context.Background()
	log, _, _ := replayEnv(t)
	for _, p := range []string{"z.txt", "a.txt", "m.txt"} {
		e, _ := ingestEntry(t, p, "h-"+p)
		rec, err := log.AppendIntent(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		if err := log.MarkDone(ctx, rec.Entry.OpID); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := log.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := &catalog.Catalog{Version: catalog.SchemaVersion, Node: testNode}
	r1, err := ProjectCatalog(base, recs, testNode)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := ProjectCatalog(base, recs, testNode)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := catalog.Encode(r1)
	b2, _ := catalog.Encode(r2)
	if !bytes.Equal(b1, b2) {
		t.Fatal("two rebuilds of the same WAL are not byte-identical")
	}
	// header carried over from base.
	if r1.Version != catalog.SchemaVersion || r1.Node != testNode {
		t.Errorf("header not carried from base: %+v", r1)
	}
}

// TestProjectCatalogPreservesFederationHeader: the base's roster/fed_id ride
// through a rebuild (only the file list is reprojected).
func TestProjectCatalogPreservesFederationHeader(t *testing.T) {
	ctx := context.Background()
	log, _, _ := replayEnv(t)
	e, _ := ingestEntry(t, "a.txt", "aaa")
	rec, err := log.AppendIntent(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.MarkDone(ctx, rec.Entry.OpID); err != nil {
		t.Fatal(err)
	}
	recs, err := log.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := &catalog.Catalog{
		Version:    catalog.SchemaVersion,
		VaultName:  "demo",
		Node:       testNode,
		Federation: catalog.Federation{FedID: "fed-xyz", Members: []catalog.Member{{Name: "home", Node: "pi", Status: catalog.StatusActive}}},
	}
	rebuilt, err := ProjectCatalog(base, recs, testNode)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.VaultName != "demo" || rebuilt.Federation.FedID != "fed-xyz" || len(rebuilt.Federation.Members) != 1 {
		t.Fatalf("federation header not preserved: %+v", rebuilt)
	}
}

// TestProjectCatalogProjectsGC (fix-35-A): a WAL that has run gc must PROJECT the
// OpGC record — removing the doomed entries by id∈BlobRefs — not error on it.
func TestProjectCatalogProjectsGC(t *testing.T) {
	ctx := context.Background()
	log, _, _ := replayEnv(t)
	keepE, _ := ingestEntry(t, "keep.bin", "keep")
	doomE, doomID := ingestEntry(t, "doomed.bin", "doom")
	for _, e := range []wal.Entry{keepE, doomE} {
		rec, err := log.AppendIntent(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		if err := log.MarkDone(ctx, rec.Entry.OpID); err != nil {
			t.Fatal(err)
		}
	}
	gcRec, err := log.AppendIntent(ctx, wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpGC, BlobRefs: []string{doomID}, Actor: "gc", CreatedAt: replayClock()()})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.MarkDone(ctx, gcRec.Entry.OpID); err != nil {
		t.Fatal(err)
	}
	recs, _ := log.Read(ctx)
	rebuilt, err := ProjectCatalog(&catalog.Catalog{Version: catalog.SchemaVersion, Node: testNode}, recs, testNode)
	if err != nil {
		t.Fatalf("ProjectCatalog must not error on a WAL with gc: %v", err)
	}
	if _, ok := rebuilt.Find("keep.bin"); !ok {
		t.Error("kept file missing")
	}
	if _, ok := rebuilt.Find("doomed.bin"); ok {
		t.Error("gc-deleted file resurrected by rebuild")
	}
}

// TestProjectCatalogRestoreRealWriter (fix-35-A/B) drives the REAL writer
// (ingest.RestoreIdentity) so a writer/projector key drift would fail it: after a
// restore, ProjectCatalog rebuilds the entry with the restored id AND a genesis
// that self-certifies it.
func TestProjectCatalogRestoreRealWriter(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	be := backend.NewFSBackend(root)
	log := &wal.Log{B: be}
	cat := &catalog.Catalog{Version: catalog.SchemaVersion, Node: testNode}

	// Seed an entry via a real ingest WAL op so projection has it pre-restore.
	ingE, _ := ingestEntry(t, "clip.mp4", "v1")
	rec, err := log.AppendIntent(ctx, ingE)
	if err != nil {
		t.Fatal(err)
	}
	catPath := filepath.Join(root, "meta", "catalog.toml")
	if err := ReplayOp(ctx, log, cat, catPath, testNode, rec, replayClock()); err != nil {
		t.Fatal(err)
	}

	// The original (off-node) identity to restore.
	g := identity.Genesis{ContentSHA256: hexSha("orig"), OriginalPath: "orig/clip.mp4", IngestOpID: ingestOpID("orig/clip.mp4"), OriginNode: "home-pi"}
	origID, err := identity.MintID(g)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreIdentity(ctx, RestoreOpts{Backend: be, Log: log, Cat: cat, Node: testNode, Actor: "a", Now: replayClock()}, "clip.mp4", origID, g); err != nil {
		t.Fatalf("RestoreIdentity (real writer): %v", err)
	}

	recs, _ := log.Read(ctx)
	rebuilt, err := ProjectCatalog(&catalog.Catalog{Version: catalog.SchemaVersion, Node: testNode}, recs, testNode)
	if err != nil {
		t.Fatalf("ProjectCatalog: %v", err)
	}
	f, ok := rebuilt.Find("clip.mp4")
	if !ok || f.ID != origID {
		t.Fatalf("restore not projected: %+v ok=%v", f, ok)
	}
	if f.Genesis != catalog.Genesis(g) {
		t.Errorf("restored genesis not reconstructed: %+v", f.Genesis)
	}
	if got, _ := identity.MintID(identity.Genesis(f.Genesis)); got != f.ID {
		t.Errorf("rebuilt entry does not self-certify: genesis mints %s, id %s", got, f.ID)
	}
}

// TestProjectCatalogCrossMoveArgs (fix-35-D) projects the cross-member move op
// shapes the merged vault_mv writer emits (canonical un-prefixed genesis keys):
// the source record (moved_to set) drops the entry; the dest record (id+dest_path
// +genesis) reconstructs it.
func TestProjectCatalogCrossMoveArgs(t *testing.T) {
	ctx := context.Background()
	g := identity.Genesis{ContentSHA256: hexSha("mv"), OriginalPath: "a.bin", IngestOpID: ingestOpID("a.bin"), OriginNode: "src"}
	fileID, _ := identity.MintID(g)

	// dest node: only the arrival OpMove record (no prior ingest).
	destLog, _, _ := replayEnv(t)
	dm, err := destLog.AppendIntent(ctx, wal.Entry{
		OpID: wal.NewOpID(), OpType: wal.OpMove, BlobRefs: []string{fileID}, Actor: "a", CreatedAt: replayClock()(),
		Args: map[string]string{
			"id": fileID, "from": "src", "src_path": "a.bin", "dest_path": "moved/a.bin",
			"content_sha256": g.ContentSHA256, "original_path": g.OriginalPath,
			"ingest_op_id": g.IngestOpID, "origin_node": g.OriginNode,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := destLog.MarkDone(ctx, dm.Entry.OpID); err != nil {
		t.Fatal(err)
	}
	destRecs, _ := destLog.Read(ctx)
	destCat, err := ProjectCatalog(&catalog.Catalog{Version: catalog.SchemaVersion, Node: "dst"}, destRecs, "dst")
	if err != nil {
		t.Fatal(err)
	}
	f, ok := destCat.Find("moved/a.bin")
	if !ok || f.ID != fileID || f.Genesis != catalog.Genesis(g) {
		t.Fatalf("cross-move dest row not reconstructed from canonical args: %+v ok=%v", f, ok)
	}

	// source node: ingest then a moved_to record → entry dropped.
	srcLog, srcCat, srcPath := replayEnv(t)
	ingE := wal.Entry{OpID: ingestOpID("a.bin"), OpType: wal.OpIngest, BlobRefs: []string{fileID}, Actor: "a", CreatedAt: replayClock()(),
		Args: map[string]string{"path": "a.bin", "content_sha256": g.ContentSHA256, "origin_node": "src", "sync_mode": catalog.SyncModeManual, "size": "5"}}
	rec, _ := srcLog.AppendIntent(ctx, ingE)
	if err := ReplayOp(ctx, srcLog, srcCat, srcPath, "src", rec, replayClock()); err != nil {
		t.Fatal(err)
	}
	sm, _ := srcLog.AppendIntent(ctx, wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpMove, BlobRefs: []string{fileID}, Actor: "a", CreatedAt: replayClock()(),
		Args: map[string]string{"from": "src", "to": "dst", "moved_to": "dst", "src_path": "a.bin", "dest_path": "moved/a.bin"}})
	if err := srcLog.MarkDone(ctx, sm.Entry.OpID); err != nil {
		t.Fatal(err)
	}
	srcRecs, _ := srcLog.Read(ctx)
	rebuiltSrc, err := ProjectCatalog(&catalog.Catalog{Version: catalog.SchemaVersion, Node: "src"}, srcRecs, "src")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rebuiltSrc.Find("a.bin"); ok {
		t.Error("cross-move source must drop the entry (file left the node)")
	}
}

// TestProjectCatalogSyncModeArgs (fix-35-D) projects OpSyncMode using the merged
// writer keys (to_mode/new_sha256/last_scanned).
func TestProjectCatalogSyncModeArgs(t *testing.T) {
	ctx := context.Background()
	log, cat, catPath := replayEnv(t)
	ingE, _ := ingestEntry(t, "f.bin", "x")
	rec, _ := log.AppendIntent(ctx, ingE)
	if err := ReplayOp(ctx, log, cat, catPath, testNode, rec, replayClock()); err != nil {
		t.Fatal(err)
	}
	sm, _ := log.AppendIntent(ctx, wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpSyncMode, BlobRefs: []string{ingE.BlobRefs[0]}, Actor: "a", CreatedAt: replayClock()(),
		Args: map[string]string{"id": ingE.BlobRefs[0], "path": "f.bin", "from_mode": "manual", "to_mode": "git", "new_sha256": hexSha("new"), "last_scanned": "2026-06-11T12:00:00Z"}})
	if err := log.MarkDone(ctx, sm.Entry.OpID); err != nil {
		t.Fatal(err)
	}
	recs, _ := log.Read(ctx)
	rebuilt, err := ProjectCatalog(&catalog.Catalog{Version: catalog.SchemaVersion, Node: testNode}, recs, testNode)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := rebuilt.Find("f.bin")
	if f.SyncMode != "git" || f.SHA256 != hexSha("new") {
		t.Fatalf("sync-mode not projected from to_mode/new_sha256: %+v", f)
	}
}

// TestProjectCatalogSkipsPasswd: a passwd op (no file-list effect) is skipped.
func TestProjectCatalogSkipsPasswd(t *testing.T) {
	ctx := context.Background()
	log, _, _ := replayEnv(t)
	ingE, _ := ingestEntry(t, "a.bin", "x")
	rec, _ := log.AppendIntent(ctx, ingE)
	if err := log.MarkDone(ctx, rec.Entry.OpID); err != nil {
		t.Fatal(err)
	}
	pw, _ := log.AppendIntent(ctx, wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpPasswd, BlobRefs: []string{"meta/auth/passwd"}, Actor: "a", CreatedAt: replayClock()()})
	if err := log.MarkDone(ctx, pw.Entry.OpID); err != nil {
		t.Fatal(err)
	}
	recs, _ := log.Read(ctx)
	rebuilt, err := ProjectCatalog(&catalog.Catalog{Version: catalog.SchemaVersion, Node: testNode}, recs, testNode)
	if err != nil {
		t.Fatalf("passwd op must be skipped, not error: %v", err)
	}
	if len(rebuilt.Files) != 1 {
		t.Errorf("passwd op must not affect the file list, got %d", len(rebuilt.Files))
	}
}
