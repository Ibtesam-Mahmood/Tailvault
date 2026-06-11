package wal

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
)

func newLog(t *testing.T) (*Log, *backend.FSBackend) {
	t.Helper()
	fs := backend.NewFSBackend(t.TempDir())
	return &Log{B: fs}, fs
}

func intent(opType, actor string, blobs ...string) Entry {
	return Entry{
		OpID:      NewOpID(),
		OpType:    opType,
		BlobRefs:  blobs,
		Actor:     actor,
		CreatedAt: time.Now().UTC(),
	}
}

// on-disk path of an entry slot file (FSBackend stores keys as files under Root).
func entryPath(fs *backend.FSBackend, seq uint64) string {
	return filepath.Join(fs.Root, filepath.FromSlash(entryKey(seq)))
}

func TestLifecycleIntentDone(t *testing.T) {
	ctx := context.Background()
	l, fs := newLog(t)

	e := intent(OpIngest, "ibte", "blobX")
	rec, err := l.AppendIntent(ctx, e)
	if err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if rec.State != StateIntent || rec.Entry.Seq != 0 || rec.Entry.PrevHash != ZeroHash {
		t.Fatalf("bad genesis rec: %+v", rec)
	}

	rawBefore, err := os.ReadFile(entryPath(fs, 0))
	if err != nil {
		t.Fatal(err)
	}

	if err := l.MarkDone(ctx, e.OpID); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}

	// The immutable entry file must be byte-unchanged by MarkDone.
	rawAfter, err := os.ReadFile(entryPath(fs, 0))
	if err != nil {
		t.Fatal(err)
	}
	if string(rawBefore) != string(rawAfter) {
		t.Error("entry file bytes changed by MarkDone")
	}

	recs, err := l.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(recs) != 1 || recs[0].State != StateDone {
		t.Fatalf("want 1 done rec, got %+v", recs)
	}
}

func TestChainVerifiesAndDetectsTamper(t *testing.T) {
	ctx := context.Background()
	l, fs := newLog(t)

	for i := 0; i < 3; i++ {
		if _, err := l.AppendIntent(ctx, intent(OpIngest, "a", "blob"+string(rune('A'+i)))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if recs, err := l.Read(ctx); err != nil || len(recs) != 3 {
		t.Fatalf("Read after 3 appends: %d recs, err %v", len(recs), err)
	}

	// Flip a byte in the middle entry → ErrChainBroken (selfHash changes, so
	// seq 2's prev_hash no longer matches).
	p := entryPath(fs, 1)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	tampered := make([]byte, len(raw))
	copy(tampered, raw)
	// flip a byte inside the actor value region (safe: keeps valid TOML).
	for i := range tampered {
		if tampered[i] == 'a' {
			tampered[i] = 'b'
			break
		}
	}
	if err := os.WriteFile(p, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Read(ctx); !errors.Is(err, ErrChainBroken) {
		t.Fatalf("tamper: want ErrChainBroken, got %v", err)
	}
}

func TestChainDetectsDeletedMiddleEntry(t *testing.T) {
	ctx := context.Background()
	l, fs := newLog(t)
	for i := 0; i < 3; i++ {
		if _, err := l.AppendIntent(ctx, intent(OpIngest, "a", "b"+string(rune('A'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	// Delete the middle entry → seq gap → ErrChainBroken.
	if err := os.Remove(entryPath(fs, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Read(ctx); !errors.Is(err, ErrChainBroken) {
		t.Fatalf("deleted middle: want ErrChainBroken, got %v", err)
	}
}

func TestWALAsLock(t *testing.T) {
	ctx := context.Background()
	l, _ := newLog(t)

	// Pending intent on blob X.
	op1 := intent(OpMove, "a", "X")
	if _, err := l.AppendIntent(ctx, op1); err != nil {
		t.Fatalf("op1: %v", err)
	}
	// Second intent on X is blocked.
	if _, err := l.AppendIntent(ctx, intent(OpMove, "a", "X")); !errors.Is(err, ErrOpInFlight) {
		t.Fatalf("op2 on X: want ErrOpInFlight, got %v", err)
	}
	// Intent on a different blob Y proceeds.
	if _, err := l.AppendIntent(ctx, intent(OpMove, "a", "Y")); err != nil {
		t.Fatalf("op3 on Y: %v", err)
	}
	// After X completes, a new intent on X is allowed.
	if err := l.MarkDone(ctx, op1.OpID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AppendIntent(ctx, intent(OpMove, "a", "X")); err != nil {
		t.Fatalf("op4 on X after done: %v", err)
	}
}

func TestIdempotency(t *testing.T) {
	ctx := context.Background()
	l, _ := newLog(t)

	e := intent(OpIngest, "a", "X")
	if _, err := l.AppendIntent(ctx, e); err != nil {
		t.Fatal(err)
	}
	// Re-append same op id → ErrDuplicateOp + existing record.
	rec, err := l.AppendIntent(ctx, e)
	if !errors.Is(err, ErrDuplicateOp) {
		t.Fatalf("dup: want ErrDuplicateOp, got %v", err)
	}
	if rec.Entry.OpID != e.OpID {
		t.Fatalf("dup did not return existing rec: %+v", rec)
	}
	// Double MarkDone is a silent no-op.
	if err := l.MarkDone(ctx, e.OpID); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkDone(ctx, e.OpID); err != nil {
		t.Fatalf("double MarkDone: %v", err)
	}
	if recs, _ := l.Read(ctx); len(recs) != 1 || recs[0].State != StateDone {
		t.Fatalf("after double done: %+v", recs)
	}
}

func TestConcurrentAppendSameBlobSerializes(t *testing.T) {
	ctx := context.Background()
	fs := backend.NewFSBackend(t.TempDir())
	l := &Log{B: &syncBackend{b: fs}}

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = l.AppendIntent(ctx, intent(OpMove, "a", "SHARED"))
		}(i)
	}
	wg.Wait()

	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrOpInFlight):
			// expected for the losers
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("same-blob concurrency: want exactly 1 success, got %d", ok)
	}
	recs, err := l.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("want 1 entry after same-blob race, got %d", len(recs))
	}
}

func TestConcurrentAppendDistinctBlobsAllSucceed(t *testing.T) {
	ctx := context.Background()
	fs := backend.NewFSBackend(t.TempDir())
	l := &Log{B: &syncBackend{b: fs}}

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = l.AppendIntent(ctx, intent(OpMove, "a", "blob-"+string(rune('A'+i))))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("distinct-blob append %d failed: %v", i, err)
		}
	}
	recs, err := l.Read(ctx)
	if err != nil {
		t.Fatalf("Read (chain verify): %v", err)
	}
	if len(recs) != n {
		t.Fatalf("want %d contiguous entries, got %d", n, len(recs))
	}
	for i, r := range recs {
		if r.Entry.Seq != uint64(i) {
			t.Fatalf("entry %d has seq %d, expected contiguous", i, r.Entry.Seq)
		}
	}
}

func TestPrunePreservesChain(t *testing.T) {
	ctx := context.Background()
	l, _ := newLog(t)

	old := time.Now().Add(-48 * time.Hour).UTC()
	// seq0 done+old, seq1 done+old, seq2 intent+recent.
	e0 := Entry{OpID: NewOpID(), OpType: OpIngest, BlobRefs: []string{"A"}, Actor: "a", CreatedAt: old}
	e1 := Entry{OpID: NewOpID(), OpType: OpIngest, BlobRefs: []string{"B"}, Actor: "a", CreatedAt: old}
	e2 := Entry{OpID: NewOpID(), OpType: OpIngest, BlobRefs: []string{"C"}, Actor: "a", CreatedAt: time.Now().UTC()}
	for _, e := range []Entry{e0, e1, e2} {
		if _, err := l.AppendIntent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.MarkDone(ctx, e0.OpID); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkDone(ctx, e1.OpID); err != nil {
		t.Fatal(err)
	}
	// e2 stays intent.

	n, err := l.Prune(ctx, 1*time.Hour)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 pruned, got %d", n)
	}

	// Surviving chain still verifies via the PRUNED anchor.
	recs, err := l.Read(ctx)
	if err != nil {
		t.Fatalf("Read after prune: %v", err)
	}
	if len(recs) != 1 || recs[0].Entry.OpID != e2.OpID || recs[0].Entry.Seq != 2 {
		t.Fatalf("survivor wrong: %+v", recs)
	}

	// A further append continues the chain at seq 3 and still verifies.
	if _, err := l.AppendIntent(ctx, intent(OpMove, "a", "D")); err != nil {
		t.Fatalf("append after prune: %v", err)
	}
	if recs, err := l.Read(ctx); err != nil || len(recs) != 2 {
		t.Fatalf("after post-prune append: %d recs, err %v", len(recs), err)
	}
}

func TestPruneSkipsFailedAndIntent(t *testing.T) {
	ctx := context.Background()
	l, _ := newLog(t)
	old := time.Now().Add(-48 * time.Hour).UTC()
	// seq0 failed+old (must NOT be pruned — stops the run immediately).
	e0 := Entry{OpID: NewOpID(), OpType: OpIngest, BlobRefs: []string{"A"}, Actor: "a", CreatedAt: old}
	if _, err := l.AppendIntent(ctx, e0); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkFailed(ctx, e0.OpID, "boom"); err != nil {
		t.Fatal(err)
	}
	n, err := l.Prune(ctx, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("failed entry must not be pruned, got n=%d", n)
	}
}

func TestPendingFiltersByBlob(t *testing.T) {
	ctx := context.Background()
	l, _ := newLog(t)
	if _, err := l.AppendIntent(ctx, intent(OpMove, "a", "X")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AppendIntent(ctx, intent(OpMove, "a", "Y")); err != nil {
		t.Fatal(err)
	}
	all, err := l.Pending(ctx, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("Pending(all): %d, %v", len(all), err)
	}
	onlyX, err := l.Pending(ctx, "X")
	if err != nil || len(onlyX) != 1 || onlyX[0].Entry.BlobRefs[0] != "X" {
		t.Fatalf("Pending(X): %+v, %v", onlyX, err)
	}
}

// Prune advances a forward-only anchor across multiple runs; the chain stays
// verifiable and an append after a full prune anchors to the latest marker.
func TestPruneForwardOnlyAnchor(t *testing.T) {
	ctx := context.Background()
	l, fs := newLog(t)
	old := time.Now().Add(-48 * time.Hour).UTC()

	appendDoneOld := func(blob string) {
		e := Entry{OpID: NewOpID(), OpType: OpIngest, BlobRefs: []string{blob}, Actor: "a", CreatedAt: old}
		if _, err := l.AppendIntent(ctx, e); err != nil {
			t.Fatal(err)
		}
		if err := l.MarkDone(ctx, e.OpID); err != nil {
			t.Fatal(err)
		}
	}

	// Round 1: 3 done+old entries → prune all → anchor seq 2.
	appendDoneOld("A")
	appendDoneOld("B")
	appendDoneOld("C")
	if n, err := l.Prune(ctx, time.Hour); err != nil || n != 3 {
		t.Fatalf("prune 1: n=%d err=%v", n, err)
	}
	if recs, err := l.Read(ctx); err != nil || len(recs) != 0 {
		t.Fatalf("after prune 1: %d recs err %v", len(recs), err)
	}

	// Round 2: more done+old entries (anchor to seq 2) → prune again → anchor seq 5.
	appendDoneOld("D")
	appendDoneOld("E")
	appendDoneOld("F")
	if n, err := l.Prune(ctx, time.Hour); err != nil || n != 3 {
		t.Fatalf("prune 2: n=%d err=%v", n, err)
	}

	// Exactly one anchor marker remains (the superseded one was cleaned up), and
	// the chain still verifies; a fresh append anchors to it.
	keys, _ := fs.List(ctx, "meta/wal/pruned/")
	if len(keys) != 1 {
		t.Fatalf("want exactly 1 live anchor marker, got %v", keys)
	}
	if _, err := l.AppendIntent(ctx, intent(OpMove, "a", "G")); err != nil {
		t.Fatalf("append after prune: %v", err)
	}
	recs, err := l.Read(ctx)
	if err != nil || len(recs) != 1 || recs[0].Entry.Seq != 6 {
		t.Fatalf("post-prune chain: %+v err %v", recs, err)
	}

	// Safety: even if the live anchor marker is lost, deleting it must not be the
	// ONLY thing standing between a valid and a bricked chain mid-prune — verified
	// structurally by writing the new anchor BEFORE deleting entries/old anchors.
}

// --- helpers ---

// syncBackend serializes individual backend calls so concurrency tests do not
// trip -race on FSBackend's non-atomic counters, while still allowing the
// logical interleaving of AppendIntent's multi-call read→put window across
// goroutines (which is what exercises the WAL-as-lock race resolution).
type syncBackend struct {
	mu sync.Mutex
	b  backend.Backend
}

func (s *syncBackend) Stat(ctx context.Context, key string) (backend.Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Stat(ctx, key)
}
func (s *syncBackend) Get(ctx context.Context, key string, w io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Get(ctx, key, w)
}
func (s *syncBackend) Put(ctx context.Context, key string, r io.Reader) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Put(ctx, key, r)
}
func (s *syncBackend) PutOverwrite(ctx context.Context, key string, r io.Reader) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.PutOverwrite(ctx, key, r)
}
func (s *syncBackend) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Delete(ctx, key)
}
func (s *syncBackend) List(ctx context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.List(ctx, prefix)
}
func (s *syncBackend) HashObject(ctx context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.HashObject(ctx, key)
}
