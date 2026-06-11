package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

func trackEnv(t *testing.T) (*backend.FSBackend, TrackOpts) {
	t.Helper()
	be := backend.NewFSBackend(t.TempDir())
	return be, TrackOpts{
		Backend: be,
		Log:     &wal.Log{B: be},
		Cat:     &catalog.Catalog{Version: catalog.SchemaVersion, Node: testNode},
		Node:    testNode,
		Actor:   "tester",
		Now:     func() time.Time { return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC) },
	}
}

// put a manual file at a vault-relative key (its logical path on the node).
func putAt(t *testing.T, be *backend.FSBackend, rel, content string) {
	t.Helper()
	if err := be.PutOverwrite(context.Background(), rel, bytes.NewReader([]byte(content))); err != nil {
		t.Fatal(err)
	}
}

func TestTrackRegistersNewFile(t *testing.T) {
	ctx := context.Background()
	be, o := trackEnv(t)
	putAt(t, be, "media/demo.mp4", "video-bytes")

	res, err := Track(ctx, o, []string{"media/demo.mp4"})
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if len(res) != 1 || res[0].Status != StatusTracked {
		t.Fatalf("result = %+v", res)
	}

	// catalog entry: self-certifying id, manual mode.
	f, ok := o.Cat.Find("media/demo.mp4")
	if !ok || f.SyncMode != catalog.SyncModeManual {
		t.Fatalf("catalog entry wrong: %+v ok=%v", f, ok)
	}
	wantID, _ := identity.MintID(identity.Genesis{
		ContentSHA256: f.Genesis.ContentSHA256, OriginalPath: f.Genesis.OriginalPath,
		IngestOpID: f.Genesis.IngestOpID, OriginNode: f.Genesis.OriginNode,
	})
	if f.ID != wantID || f.ID != res[0].ID {
		t.Fatalf("id not self-certifying: f.ID=%s want=%s res=%s", f.ID, wantID, res[0].ID)
	}

	// WAL: one done ingest catch-up entry; catalog persisted over the backend.
	recs, _ := o.Log.Read(ctx)
	if len(recs) != 1 || recs[0].Entry.OpType != wal.OpIngest || recs[0].State != wal.StateDone {
		t.Fatalf("wal recs = %+v", recs)
	}
	persisted, err := catalog.Load(catPathOf(be))
	if err != nil || len(persisted.Files) != 1 {
		t.Fatalf("catalog not persisted over backend: %v %+v", err, persisted)
	}
}

func catPathOf(be *backend.FSBackend) string {
	return be.Root + "/meta/catalog.toml"
}

func TestTrackIdempotentAndDrift(t *testing.T) {
	ctx := context.Background()
	be, o := trackEnv(t)
	putAt(t, be, "a.bin", "one")

	if _, err := Track(ctx, o, []string{"a.bin"}); err != nil {
		t.Fatal(err)
	}
	recsBefore, _ := o.Log.Read(ctx)

	// Re-track unchanged → no-op, no new WAL entry, same id.
	res, err := Track(ctx, o, []string{"a.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Status != StatusAlready {
		t.Fatalf("re-track status = %s, want already-tracked", res[0].Status)
	}
	recsAfter, _ := o.Log.Read(ctx)
	if len(recsAfter) != len(recsBefore) {
		t.Errorf("re-track minted a new WAL entry: %d → %d", len(recsBefore), len(recsAfter))
	}

	// Edit bytes on disk (drift) → track NOTES drift but doesn't re-hash into the
	// catalog (scan's job).
	putAt(t, be, "a.bin", "two-different")
	res, err = Track(ctx, o, []string{"a.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Status != StatusDrifted {
		t.Fatalf("drift status = %s, want drifted", res[0].Status)
	}
	f, _ := o.Cat.Find("a.bin")
	if f.SHA256 != sha256Of("one") {
		t.Errorf("track must not absorb drift: catalog sha = %s", f.SHA256)
	}
}

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestTrackMissingPathErrors(t *testing.T) {
	ctx := context.Background()
	_, o := trackEnv(t)
	if _, err := Track(ctx, o, []string{"nope.bin"}); err == nil {
		t.Fatal("track of a missing path must error")
	}
}

func TestTrackInterruptedBatchResumes(t *testing.T) {
	ctx := context.Background()
	be, o := trackEnv(t)
	putAt(t, be, "x.bin", "xx")
	putAt(t, be, "y.bin", "yy")

	// First pass tracks x.bin only (simulate interruption after one file).
	if _, err := Track(ctx, o, []string{"x.bin"}); err != nil {
		t.Fatal(err)
	}
	// Resume with both: x already tracked (no re-mint), y newly tracked.
	res, err := Track(ctx, o, []string{"x.bin", "y.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Status != StatusAlready || res[1].Status != StatusTracked {
		t.Fatalf("resume statuses = %s, %s", res[0].Status, res[1].Status)
	}
	if len(o.Cat.Files) != 2 {
		t.Fatalf("want 2 catalog files, got %d", len(o.Cat.Files))
	}
}
