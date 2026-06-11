package ingest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// replayEnv gives a log + catalog + catPath rooted at a fresh temp dir.
func replayEnv(t *testing.T) (*wal.Log, *catalog.Catalog, string) {
	t.Helper()
	root := t.TempDir()
	catPath := filepath.Join(root, "meta", "catalog.toml")
	return &wal.Log{B: backend.NewFSBackend(root)}, &catalog.Catalog{Version: catalog.SchemaVersion, Node: testNode}, catPath
}

func replayClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC) }
}

// A crash-after-intent ingest op: ReplayOp finishes it (catalog row + done).
func TestReplayIngestCompletesPendingOp(t *testing.T) {
	ctx := context.Background()
	log, cat, catPath := replayEnv(t)

	g := identity.Genesis{
		ContentSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		OriginalPath:  "a.txt", IngestOpID: ingestOpID("a.txt"), OriginNode: testNode,
	}
	id, _ := identity.MintID(g)
	rec, err := log.AppendIntent(ctx, wal.Entry{
		OpID: g.IngestOpID, OpType: wal.OpIngest, BlobRefs: []string{id}, Actor: "a", CreatedAt: replayClock()(),
		Args: map[string]string{
			"path": "a.txt", "content_sha256": g.ContentSHA256, "origin_node": testNode,
			"sync_mode": catalog.SyncModeManual, "size": "5",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// pending: not in catalog, not done.
	if _, ok := cat.Find("a.txt"); ok {
		t.Fatal("precondition: catalog should be empty")
	}

	if err := ReplayOp(ctx, log, cat, catPath, testNode, rec, replayClock()); err != nil {
		t.Fatalf("ReplayOp: %v", err)
	}

	f, ok := cat.Find("a.txt")
	if !ok || f.ID != id || f.SHA256 != g.ContentSHA256 || f.SyncMode != catalog.SyncModeManual {
		t.Fatalf("replayed ingest row wrong: %+v ok=%v", f, ok)
	}
	// persisted + marked done.
	if got := loadCat(t, filepath.Dir(filepath.Dir(catPath))); len(got.Files) != 1 {
		t.Fatalf("catalog not persisted: %+v", got.Files)
	}
	assertDone(t, log, g.IngestOpID)

	// Idempotent: a second replay is a clean no-op.
	if err := ReplayOp(ctx, log, cat, catPath, testNode, rec, replayClock()); err != nil {
		t.Fatalf("ReplayOp #2 (idempotent): %v", err)
	}
}

func TestReplayMovePreservesID(t *testing.T) {
	ctx := context.Background()
	log, cat, catPath := replayEnv(t)
	// seed an existing file at sub/c.txt.
	cat.Upsert(catalog.File{ID: "id-c", SHA256: "cc", Path: "sub/c.txt", SyncMode: catalog.SyncModeManual})

	rec, err := log.AppendIntent(ctx, wal.Entry{
		OpID: wal.NewOpID(), OpType: wal.OpMove, BlobRefs: []string{"id-c"}, Actor: "a", CreatedAt: replayClock()(),
		Args: map[string]string{"from": "sub/c.txt", "to": "moved/c.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ReplayOp(ctx, log, cat, catPath, testNode, rec, replayClock()); err != nil {
		t.Fatalf("ReplayOp move: %v", err)
	}
	if _, ok := cat.Find("sub/c.txt"); ok {
		t.Error("old path should be gone")
	}
	f, ok := cat.Find("moved/c.txt")
	if !ok || f.ID != "id-c" {
		t.Fatalf("move did not preserve id: %+v ok=%v", f, ok)
	}
	assertDone(t, log, rec.Entry.OpID)
}

func TestReplayDelete(t *testing.T) {
	ctx := context.Background()
	log, cat, catPath := replayEnv(t)
	cat.Upsert(catalog.File{ID: "id-d", SHA256: "dd", Path: "gone.txt", SyncMode: catalog.SyncModeManual})

	rec, err := log.AppendIntent(ctx, wal.Entry{
		OpID: wal.NewOpID(), OpType: wal.OpDelete, BlobRefs: []string{"id-d"}, Actor: "a", CreatedAt: replayClock()(),
		Args: map[string]string{"path": "gone.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ReplayOp(ctx, log, cat, catPath, testNode, rec, replayClock()); err != nil {
		t.Fatalf("ReplayOp delete: %v", err)
	}
	if _, ok := cat.Find("gone.txt"); ok {
		t.Error("delete replay did not remove the entry")
	}
	assertDone(t, log, rec.Entry.OpID)
}

func TestReplayScanEdited(t *testing.T) {
	ctx := context.Background()
	log, cat, catPath := replayEnv(t)
	cat.Upsert(catalog.File{ID: "id-e", SHA256: "old", Path: "e.txt", SyncMode: catalog.SyncModeManual})

	rec, err := log.AppendIntent(ctx, wal.Entry{
		OpID: wal.NewOpID(), OpType: wal.OpScan, BlobRefs: []string{"id-e"}, Actor: "a", CreatedAt: replayClock()(),
		Args: map[string]string{"path": "e.txt", "old_sha256": "old", "new_sha256": "new"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ReplayOp(ctx, log, cat, catPath, testNode, rec, replayClock()); err != nil {
		t.Fatalf("ReplayOp scan: %v", err)
	}
	f, _ := cat.Find("e.txt")
	if f.SHA256 != "new" || f.ID != "id-e" {
		t.Fatalf("scan edit replay wrong: %+v", f)
	}
	assertDone(t, log, rec.Entry.OpID)
}

func TestReplayUnsupportedOpType(t *testing.T) {
	ctx := context.Background()
	log, cat, catPath := replayEnv(t)
	rec, err := log.AppendIntent(ctx, wal.Entry{
		OpID: wal.NewOpID(), OpType: wal.OpGC, BlobRefs: []string{"x"}, Actor: "a", CreatedAt: replayClock()(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ReplayOp(ctx, log, cat, catPath, testNode, rec, replayClock()); err == nil {
		t.Fatal("expected an error for an unsupported op_type (gc)")
	}
}

func assertDone(t *testing.T, log *wal.Log, opID string) {
	t.Helper()
	recs, err := log.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Entry.OpID == opID {
			if r.State != wal.StateDone {
				t.Fatalf("op %s state = %s, want done", opID, r.State)
			}
			return
		}
	}
	t.Fatalf("op %s not found", opID)
}
