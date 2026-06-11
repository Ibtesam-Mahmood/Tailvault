package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/ops"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

func opRec(opID, opType string, state wal.State, blobs ...string) wal.Rec {
	return wal.Rec{Entry: wal.Entry{OpID: opID, OpType: opType, BlobRefs: blobs, CreatedAt: time.Unix(1000, 0).UTC()}, State: state}
}

func TestGCOpExecutor_Retry_FinishesJournaledSweep(t *testing.T) {
	ctx := context.Background()
	be := backend.NewFSBackend(t.TempDir())
	if err := be.Put(ctx, "objects/shaG", strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	cat := &catalog.Catalog{Version: 2, VaultName: "v", Node: "n",
		Files: []catalog.File{{ID: "idG", SHA256: "shaG", Path: "g.bin", SyncMode: "git"}}}
	log := &wal.Log{B: be}
	// Simulate a crashed sweep: a pending gc intent locking idG, no done marker.
	rec, err := log.AppendIntent(ctx, wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpGC, BlobRefs: []string{"idG"}, Actor: "t"})
	if err != nil {
		t.Fatal(err)
	}

	ex := &gcOpExecutor{be: be, cat: cat, log: log}
	if v, _, _ := ex.Diagnose(ctx, ops.PendingOp{Rec: rec}); v != ops.Retryable {
		t.Errorf("gc Diagnose = %v, want retryable", v)
	}
	if err := ex.Retry(ctx, ops.PendingOp{Member: "m", Rec: rec}); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	if m, _ := be.Stat(ctx, "objects/shaG"); m.Exists {
		t.Error("doomed blob should be deleted by gc op replay")
	}
	if _, ok := cat.Find("g.bin"); ok {
		t.Error("doomed file should be removed from the catalog")
	}
	// Catalog persisted over the backend (atomic PutOverwrite).
	var buf bytes.Buffer
	if err := be.Get(ctx, "meta/catalog.toml", &buf); err != nil {
		t.Fatalf("catalog not persisted: %v", err)
	}
	// gc op now done.
	recs, err := log.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var done bool
	for _, r := range recs {
		if r.Entry.OpType == wal.OpGC && r.State == wal.StateDone {
			done = true
		}
	}
	if !done {
		t.Error("gc op should be marked done after retry")
	}
}

func TestGCOpExecutor_Retry_IdempotentOnAlreadyRemoved(t *testing.T) {
	ctx := context.Background()
	be := backend.NewFSBackend(t.TempDir())
	// Catalog no longer holds the id (original sweep already removed it) — replay
	// must be a clean no-op delete + done, not an error.
	cat := &catalog.Catalog{Version: 2, VaultName: "v", Node: "n"}
	log := &wal.Log{B: be}
	rec, err := log.AppendIntent(ctx, wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpGC, BlobRefs: []string{"gone-id"}, Actor: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ex := &gcOpExecutor{be: be, cat: cat, log: log}
	if err := ex.Retry(ctx, ops.PendingOp{Rec: rec}); err != nil {
		t.Fatalf("idempotent gc retry should succeed: %v", err)
	}
}

func TestFindByPrefix(t *testing.T) {
	list := []ops.PendingOp{
		{Rec: opRec("aaaa1111bbbb2222", wal.OpIngest, wal.StateIntent)},
		{Rec: opRec("aaaa9999cccc3333", wal.OpMove, wal.StateIntent)},
		{Rec: opRec("ffff0000dddd4444", wal.OpDelete, wal.StateFailed)},
	}
	if _, err := findByPrefix(list, "ffff0000"); err != nil {
		t.Errorf("unique prefix should resolve: %v", err)
	}
	if _, err := findByPrefix(list, "aaaa"); err == nil {
		t.Error("ambiguous prefix should error")
	}
	if _, err := findByPrefix(list, "deadbeef"); err == nil {
		t.Error("no-match prefix should error")
	}
}

func TestFilterByMember(t *testing.T) {
	list := []ops.PendingOp{
		{Member: "pi-1", Rec: opRec("a", wal.OpIngest, wal.StateIntent)},
		{Member: "pi-2", Rec: opRec("b", wal.OpMove, wal.StateIntent)},
	}
	if got := filterByMember(list, ""); len(got) != 2 {
		t.Errorf("empty filter = %d, want all", len(got))
	}
	if got := filterByMember(list, "pi-2"); len(got) != 1 || got[0].Member != "pi-2" {
		t.Errorf("member filter = %+v, want only pi-2", got)
	}
}

func TestAgeString(t *testing.T) {
	now := time.Unix(100000, 0)
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{2 * 24 * time.Hour, "2d"},
	}
	for _, c := range cases {
		if got := ageString(now, now.Add(-c.ago)); got != c.want {
			t.Errorf("ageString(%v) = %q, want %q", c.ago, got, c.want)
		}
	}
}
