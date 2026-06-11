package backend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// RunContract exercises the full Backend contract — Put/Stat/Get round-trip,
// dedup, List-by-prefix, Delete, and missing-key semantics — against any
// implementation, so SSH, FSBackend, and any future backend (e.g. Taildrive,
// task-22) are verified identically. Exported for reuse by other packages.
func RunContract(t *testing.T, b Backend) {
	t.Helper()
	ctx := context.Background()
	const key = "objects/9f2b1c"
	payload := []byte("hello tailvault content-addressed bytes")

	// Initially absent: Stat reports not-exists with no error.
	if m, err := b.Stat(ctx, key); err != nil || m.Exists {
		t.Fatalf("Stat(absent) = %+v, %v; want {Exists:false}, nil", m, err)
	}

	// Get of an absent key -> TV-OBJ-01 wrapping ErrNotExist.
	if err := b.Get(ctx, key, &bytes.Buffer{}); !isObjMissing(err) {
		t.Fatalf("Get(absent): want TV-OBJ-01/ErrNotExist, got %v", err)
	}

	// Put then Stat reflects existence + size.
	if err := b.Put(ctx, key, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	m, err := b.Stat(ctx, key)
	if err != nil || !m.Exists || m.Size != int64(len(payload)) {
		t.Fatalf("Stat(present) = %+v, %v; want {Exists:true, Size:%d}", m, err, len(payload))
	}

	// Get round-trips identical bytes.
	var got bytes.Buffer
	if err := b.Get(ctx, key, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("Get bytes = %q, want %q", got.Bytes(), payload)
	}

	// HashObject returns the known digest of the stored bytes.
	wantDigest := sha256Hex(payload)
	if d, err := b.HashObject(ctx, key); err != nil || d != wantDigest {
		t.Fatalf("HashObject(present) = %q, %v; want %q, nil", d, err, wantDigest)
	}
	// HashObject of an absent key -> TV-OBJ-01 wrapping ErrNotExist.
	if _, err := b.HashObject(ctx, "objects/absent-hash"); !isObjMissing(err) {
		t.Fatalf("HashObject(absent): want TV-OBJ-01/ErrNotExist, got %v", err)
	}

	// List by prefix finds it; an unrelated prefix does not.
	keys, err := b.List(ctx, "objects/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !contains(keys, key) {
		t.Fatalf("List(objects/) = %v, want to contain %q", keys, key)
	}
	if other, _ := b.List(ctx, "refs/"); contains(other, key) {
		t.Fatalf("List(refs/) = %v, should not contain %q", other, key)
	}

	// Delete removes it; Stat then reports not-exists.
	if err := b.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if m, err := b.Stat(ctx, key); err != nil || m.Exists {
		t.Fatalf("Stat(after delete) = %+v, %v; want {Exists:false}, nil", m, err)
	}
	// Delete of an absent key is a no-op (rm -f semantics).
	if err := b.Delete(ctx, key); err != nil {
		t.Fatalf("Delete(absent): want nil, got %v", err)
	}
}

func TestFSBackend_Contract(t *testing.T) {
	RunContract(t, NewFSBackend(t.TempDir()))
}

func TestFSBackend_PutDedup(t *testing.T) {
	ctx := context.Background()
	b := NewFSBackend(t.TempDir())
	const key = "objects/dedupe"
	body := []byte("same bytes")

	if err := b.Put(ctx, key, bytes.NewReader(body)); err != nil {
		t.Fatalf("Put #1: %v", err)
	}
	if b.Puts != 1 {
		t.Fatalf("after first Put, Puts = %d, want 1", b.Puts)
	}
	// Re-Put of an already-present key must transfer/write nothing.
	if err := b.Put(ctx, key, bytes.NewReader(body)); err != nil {
		t.Fatalf("Put #2: %v", err)
	}
	if b.Puts != 1 {
		t.Errorf("after dedup Put, Puts = %d, want still 1", b.Puts)
	}
}

func TestFSBackend_MisKeyedBlobAllowed(t *testing.T) {
	// verify (task-23) needs to plant a blob whose bytes do NOT hash to its key.
	ctx := context.Background()
	b := NewFSBackend(t.TempDir())
	const key = "objects/deadbeef"
	if err := b.Put(ctx, key, bytes.NewReader([]byte("not the right bytes"))); err != nil {
		t.Fatalf("Put mis-keyed: %v", err)
	}
	sum, err := b.HashObject(ctx, key)
	if err != nil {
		t.Fatalf("HashObject: %v", err)
	}
	if strings.HasPrefix(key, "objects/"+sum) {
		t.Errorf("expected bytes NOT to hash to key; got %s", sum)
	}
}

func TestFSBackend_DeleteCounter(t *testing.T) {
	ctx := context.Background()
	b := NewFSBackend(t.TempDir())
	const key = "objects/k"
	_ = b.Put(ctx, key, bytes.NewReader([]byte("x")))

	if b.Deletes != 0 {
		t.Fatalf("Deletes = %d before any delete, want 0", b.Deletes)
	}
	if err := b.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if b.Deletes != 1 {
		t.Errorf("Deletes = %d after one real delete, want 1", b.Deletes)
	}
	// Deleting an absent key does not bump the counter.
	_ = b.Delete(ctx, key)
	if b.Deletes != 1 {
		t.Errorf("Deletes = %d after no-op delete, want still 1", b.Deletes)
	}
}

func TestFSBackend_ErrNotExistSentinel(t *testing.T) {
	b := NewFSBackend(t.TempDir())
	err := b.Get(context.Background(), "objects/missing", &bytes.Buffer{})
	if !errors.Is(err, ErrNotExist) {
		t.Errorf("errors.Is(err, ErrNotExist) = false; got %v", err)
	}
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.ObjMissing {
		t.Errorf("errors.As tserr TV-OBJ-01 failed; got %v", err)
	}
}

func isObjMissing(err error) bool {
	if !errors.Is(err, ErrNotExist) {
		return false
	}
	var te *tserr.Error
	return errors.As(err, &te) && te.Code == tserr.ObjMissing
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
