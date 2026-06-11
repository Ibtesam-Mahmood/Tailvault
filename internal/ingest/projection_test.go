package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

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
