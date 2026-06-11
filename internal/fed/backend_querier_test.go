package fed

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

const emptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func newFS(t *testing.T) *backend.FSBackend {
	t.Helper()
	return backend.NewFSBackend(t.TempDir())
}

func backendForFixed(b backend.Backend) BackendFor {
	return func(catalog.Member) (backend.Backend, error) { return b, nil }
}

func catFile(id, path string) catalog.File {
	return catalog.File{
		ID:          id,
		Genesis:     catalog.Genesis{ContentSHA256: emptySHA, OriginalPath: path, IngestOpID: "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6", OriginNode: "home-pi"},
		SHA256:      emptySHA,
		Path:        path,
		SyncMode:    "manual",
		Size:        10,
		CreatedAt:   tm("2026-06-11T09:10:00Z"),
		UpdatedAt:   tm("2026-06-11T09:10:00Z"),
		LastScanned: tm("2026-06-11T09:10:00Z"),
	}
}

func putCatalog(t *testing.T, b backend.Backend, files ...catalog.File) {
	t.Helper()
	c := &catalog.Catalog{
		Version:    2,
		VaultName:  "test-vault",
		Node:       "home-pi",
		Federation: catalog.Federation{FedID: "fed-1"},
		Files:      files,
	}
	bs, err := catalog.Encode(c)
	if err != nil {
		t.Fatalf("catalog.Encode: %v", err)
	}
	if err := b.Put(context.Background(), catalogKey, bytes.NewReader(bs)); err != nil {
		t.Fatalf("put catalog: %v", err)
	}
}

// seedMove appends a move WAL op on id; done marks it completed. movedTo, when
// set, is the cross-member forwarding target (args["moved_to"]); when empty the
// op is a local rename (from/to only).
func seedMove(t *testing.T, b backend.Backend, id, movedTo string, done bool) {
	t.Helper()
	log := &wal.Log{B: b}
	e := wal.Entry{
		OpID:     wal.NewOpID(),
		OpType:   wal.OpMove,
		BlobRefs: []string{id},
		Actor:    "tester",
		Args:     map[string]string{"from": "old/path", "to": "new/path"},
	}
	if movedTo != "" {
		e.Args["moved_to"] = movedTo
	}
	rec, err := log.AppendIntent(context.Background(), e)
	if err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if done {
		if err := log.MarkDone(context.Background(), rec.Entry.OpID); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}
	}
}

func TestBackendQuerier_Found(t *testing.T) {
	b := newFS(t)
	putCatalog(t, b, catFile(tid, "pnp/board.pdf"))
	q := NewBackendQuerier(backendForFixed(b))

	v, err := q.Query(context.Background(), member("pi-1"), tid)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Found || v.Member != "pi-1" || v.File.ID != tid {
		t.Errorf("got %+v, want Found pi-1 %s", v, tid)
	}
}

func TestBackendQuerier_NotFound_NoWAL(t *testing.T) {
	b := newFS(t)
	putCatalog(t, b) // empty catalog
	q := NewBackendQuerier(backendForFixed(b))

	v, err := q.Query(context.Background(), member("pi-1"), tid)
	if err != nil {
		t.Fatal(err)
	}
	if v.Found || v.MovedTo != "" || v.PendingMove {
		t.Errorf("got %+v, want empty not-found view", v)
	}
}

func TestBackendQuerier_PendingMove(t *testing.T) {
	b := newFS(t)
	putCatalog(t, b) // not in catalog (or could be; intent doesn't remove it)
	seedMove(t, b, tid, "pi-2", false /* intent, not done */)
	q := NewBackendQuerier(backendForFixed(b))

	v, err := q.Query(context.Background(), member("pi-1"), tid)
	if err != nil {
		t.Fatal(err)
	}
	if !v.PendingMove {
		t.Errorf("got %+v, want PendingMove=true", v)
	}
}

func TestBackendQuerier_CompletedCrossMemberMove(t *testing.T) {
	b := newFS(t)
	putCatalog(t, b) // no longer held here
	seedMove(t, b, tid, "office-nas", true /* done */)
	q := NewBackendQuerier(backendForFixed(b))

	v, err := q.Query(context.Background(), member("pi-1"), tid)
	if err != nil {
		t.Fatal(err)
	}
	if v.MovedTo != "office-nas" {
		t.Errorf("got MovedTo=%q, want office-nas", v.MovedTo)
	}
	if v.Found || v.PendingMove {
		t.Errorf("completed cross-member move should be a pure forwarding pointer: %+v", v)
	}
}

func TestBackendQuerier_LocalRenameIsNotAPointer(t *testing.T) {
	b := newFS(t)
	putCatalog(t, b)
	seedMove(t, b, tid, "" /* no moved_to → local rename */, true)
	q := NewBackendQuerier(backendForFixed(b))

	v, err := q.Query(context.Background(), member("pi-1"), tid)
	if err != nil {
		t.Fatal(err)
	}
	if v.MovedTo != "" {
		t.Errorf("a local rename (from/to only) must NOT be a forwarding pointer; got MovedTo=%q", v.MovedTo)
	}
}

func TestBackendQuerier_NoCatalog_HoldsNothing(t *testing.T) {
	b := newFS(t) // nothing seeded at all
	q := NewBackendQuerier(backendForFixed(b))

	v, err := q.Query(context.Background(), member("pi-1"), tid)
	if err != nil {
		t.Fatalf("missing catalog must not error: %v", err)
	}
	if v.Found || v.MovedTo != "" || v.PendingMove {
		t.Errorf("got %+v, want empty view for an un-catalogued member", v)
	}
}

func TestBackendQuerier_BackendError(t *testing.T) {
	want := errors.New("registry: unknown member")
	q := NewBackendQuerier(func(catalog.Member) (backend.Backend, error) { return nil, want })

	_, err := q.Query(context.Background(), member("pi-1"), tid)
	if !errors.Is(err, want) {
		t.Errorf("backendFor error must propagate; got %v", err)
	}
}

func TestBackendQuerier_ChainBrokenWrapped(t *testing.T) {
	b := newFS(t)
	putCatalog(t, b) // queried id not in catalog → proceeds to WAL read
	// Two chained move intents on distinct blobs, then corrupt seq 0 so seq 1's
	// prev_hash no longer matches → wal.Read returns ErrChainBroken.
	seedMove(t, b, "aaaa", "", false)
	seedMove(t, b, "bbbb", "", false)

	entry0 := filepath.Join(b.Root, "meta", "wal", "000000000000.toml")
	raw, err := os.ReadFile(entry0)
	if err != nil {
		t.Fatal(err)
	}
	e, err := wal.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	e.Actor = "tampered" // changes seq 0's hash, breaking seq 1's prev_hash link
	reenc, err := wal.Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry0, reenc, 0o644); err != nil {
		t.Fatal(err)
	}

	q := NewBackendQuerier(backendForFixed(b))
	_, err = q.Query(context.Background(), member("pi-1"), tid)
	if !errors.Is(err, wal.ErrChainBroken) {
		t.Errorf("chain break must stay errors.Is(wal.ErrChainBroken); got %v", err)
	}
}

// BackendQuerier must satisfy the Querier interface consumed by Resolver.
var _ Querier = (*BackendQuerier)(nil)
