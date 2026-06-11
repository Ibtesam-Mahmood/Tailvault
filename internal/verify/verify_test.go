package verify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
)

func shaOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// putBlob stores content under objects/<key>. key may intentionally differ from
// the content's true hash to simulate corruption.
func putBlob(t *testing.T, b *backend.FSBackend, key string, content []byte) {
	t.Helper()
	if err := b.Put(context.Background(), "objects/"+key, bytes.NewReader(content)); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
}

func TestRun_AllGood(t *testing.T) {
	ctx := context.Background()
	b := backend.NewFSBackend(t.TempDir())
	c1, c2 := []byte("alpha"), []byte("beta")
	k1, k2 := shaOf(c1), shaOf(c2)
	putBlob(t, b, k1, c1)
	putBlob(t, b, k2, c2)
	lk := &lock.Lock{Entries: []lock.Entry{
		{Path: "a", SHA256: k1},
		{Path: "b", SHA256: k2},
	}}
	rep, err := Run(ctx, b, lk)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.OK() {
		t.Errorf("expected clean report, got corrupt=%v missing=%v", rep.Corrupt, rep.Missing)
	}
	if rep.Checked != 2 {
		t.Errorf("Checked = %d, want 2", rep.Checked)
	}
}

func TestRun_CorruptionDetected(t *testing.T) {
	ctx := context.Background()
	b := backend.NewFSBackend(t.TempDir())
	content := []byte("the right content")
	key := shaOf(content)
	// Store bytes that do NOT hash to the key.
	bad := []byte("rotten bytes")
	putBlob(t, b, key, bad)
	lk := &lock.Lock{Entries: []lock.Entry{{Path: "a", SHA256: key}}}

	rep, err := Run(ctx, b, lk)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Corrupt) != 1 {
		t.Fatalf("Corrupt = %v, want 1 finding", rep.Corrupt)
	}
	f := rep.Corrupt[0]
	if f.Key != key || f.Got != shaOf(bad) {
		t.Errorf("corruption finding = %+v, want Key=%s Got=%s", f, key, shaOf(bad))
	}
	if rep.OK() {
		t.Error("report should not be OK with a corrupt blob")
	}
}

func TestRun_MissingDetected(t *testing.T) {
	ctx := context.Background()
	b := backend.NewFSBackend(t.TempDir())
	// Lock references M, but no blob is stored.
	lk := &lock.Lock{Entries: []lock.Entry{{Path: "doc/x.pdf", SHA256: "M"}}}
	rep, err := Run(ctx, b, lk)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Missing) != 1 || rep.Missing[0].Key != "M" {
		t.Fatalf("Missing = %v, want one finding for M", rep.Missing)
	}
	if got := rep.Missing[0].Paths; len(got) != 1 || got[0] != "doc/x.pdf" {
		t.Errorf("missing finding paths = %v, want [doc/x.pdf]", got)
	}
}

func TestRun_OrphanBlobIntact(t *testing.T) {
	ctx := context.Background()
	b := backend.NewFSBackend(t.TempDir())
	content := []byte("orphan but valid")
	key := shaOf(content)
	putBlob(t, b, key, content) // present, hashes to key, but not in any lock
	lk := &lock.Lock{}          // empty lock

	rep, err := Run(ctx, b, lk)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.OK() {
		t.Errorf("intact orphan blob must not be flagged: corrupt=%v missing=%v", rep.Corrupt, rep.Missing)
	}
	if rep.Checked != 1 {
		t.Errorf("Checked = %d, want 1", rep.Checked)
	}
}

func TestRun_HistoryVersionMissing(t *testing.T) {
	ctx := context.Background()
	b := backend.NewFSBackend(t.TempDir())
	cur := []byte("current version")
	kCur := shaOf(cur)
	putBlob(t, b, kCur, cur)
	// versions = [B(current), A] where A's blob is absent.
	lk := &lock.Lock{Entries: []lock.Entry{
		{Path: "h.pdf", SHA256: kCur, History: true, Versions: []string{kCur, "A"}},
	}}
	rep, err := Run(ctx, b, lk)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Missing) != 1 || rep.Missing[0].Key != "A" {
		t.Fatalf("Missing = %v, want history version A reported", rep.Missing)
	}
}

func TestRun_UsesHashObjectNotGet(t *testing.T) {
	// The DEV-C1 short-circuit: verify must hash every blob via HashObject and
	// stream ZERO blob bytes back through Get. Asserted on the counting stub.
	ctx := context.Background()
	b := backend.NewFSBackend(t.TempDir())
	c1, c2, c3 := []byte("one"), []byte("two"), []byte("three")
	putBlob(t, b, shaOf(c1), c1)
	putBlob(t, b, shaOf(c2), c2)
	putBlob(t, b, shaOf(c3), c3)
	lk := &lock.Lock{Entries: []lock.Entry{
		{Path: "a", SHA256: shaOf(c1)},
		{Path: "b", SHA256: shaOf(c2)},
		{Path: "c", SHA256: shaOf(c3)},
	}}

	rep, err := Run(ctx, b, lk)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.OK() || rep.Checked != 3 {
		t.Fatalf("report = %+v; want clean, Checked=3", rep)
	}
	if b.Hashes != 3 {
		t.Errorf("HashObject calls = %d, want 3 (one per blob)", b.Hashes)
	}
	if b.Gets != 0 {
		t.Errorf("Get calls = %d, want 0 (verify must not stream blobs)", b.Gets)
	}
}
