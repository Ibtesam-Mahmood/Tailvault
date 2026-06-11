package push

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/gc"
	"github.com/Ibtesam-Mahmood/tailvault/internal/history"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// repo builds a temp repo with the given files and a managing config.
func repo(t *testing.T, files map[string]string) (string, *config.Config) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{
		Version: 1,
		Storage: config.Storage{Location: "home-pi"},
		Rules:   config.Rules{MinSize: "5MB", Include: []string{"**/*.pdf"}, AutoDelete: true},
	}
	return root, cfg
}

func deps(b backend.Backend) Deps {
	return Deps{
		Backend:     b,
		Preflight:   func(context.Context) error { return nil },
		Whois:       func(context.Context) (string, error) { return "ibte@laptop", nil },
		GitIdentity: func() string { return "git@fallback" },
		Now:         func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
}

func TestPush_NewFile_OnePut(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{"a.pdf": "alpha"})
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1}

	res, err := Run(ctx, root, cfg, lk, deps(b), Options{})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(res.Uploaded) != 1 || b.Puts != 1 {
		t.Fatalf("uploaded=%v Puts=%d, want 1/1", res.Uploaded, b.Puts)
	}
	// blob present + lock written
	if m, _ := b.Stat(ctx, "objects/"+sha([]byte("alpha"))); !m.Exists {
		t.Error("blob not present after push")
	}
	lk2, err := lock.Load(filepath.Join(root, "tailvault.lock"))
	if err != nil {
		t.Fatalf("lock load: %v", err)
	}
	if len(lk2.Entries) != 1 || lk2.Entries[0].Path != "a.pdf" || lk2.Entries[0].Pusher != "ibte@laptop" {
		t.Errorf("lock entry = %+v", lk2.Entries)
	}
}

func TestPush_UnchangedRepush_ZeroPut(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{"a.pdf": "alpha"})
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1}

	if _, err := Run(ctx, root, cfg, lk, deps(b), Options{}); err != nil {
		t.Fatalf("push #1: %v", err)
	}
	first, _ := os.ReadFile(filepath.Join(root, "tailvault.lock"))
	putsAfter1 := b.Puts

	lk2, _ := lock.Load(filepath.Join(root, "tailvault.lock"))
	res, err := Run(ctx, root, cfg, lk2, deps(b), Options{})
	if err != nil {
		t.Fatalf("push #2: %v", err)
	}
	if len(res.Uploaded) != 0 || b.Puts != putsAfter1 {
		t.Errorf("re-push uploaded=%v Puts=%d (was %d), want zero new", res.Uploaded, b.Puts, putsAfter1)
	}
	second, _ := os.ReadFile(filepath.Join(root, "tailvault.lock"))
	if !bytes.Equal(first, second) {
		t.Errorf("lock not byte-identical after unchanged re-push\n--first--\n%s\n--second--\n%s", first, second)
	}
}

func TestPush_ContentPresentElsewhere_ZeroPut(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{"a.pdf": "alpha"})
	b := backend.NewFSBackend(t.TempDir())
	// blob already on node under the content-addressed key.
	_ = b.Put(ctx, "objects/"+sha([]byte("alpha")), bytes.NewReader([]byte("alpha")))
	puts := b.Puts
	lk := &lock.Lock{Version: 1}

	res, err := Run(ctx, root, cfg, lk, deps(b), Options{})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(res.Uploaded) != 0 || len(res.Deduped) != 1 || b.Puts != puts {
		t.Errorf("uploaded=%v deduped=%v Puts=%d, want 0/1/%d", res.Uploaded, res.Deduped, b.Puts, puts)
	}
}

// TestPush_CleanPointer_RecordsContentSize covers the qa-review finding: when a
// new path's working file is still a clean pointer (blob already on the node),
// the lock must record the real content size from the pointer, not the ~60-byte
// pointer text length (SPEC §2).
func TestPush_CleanPointer_RecordsContentSize(t *testing.T) {
	ctx := context.Background()
	content := []byte("the real one-hundred-and-eleven-ish bytes of actual blob content goes right here yes")
	contentSHA := sha(content)
	contentSize := int64(len(content))

	root := t.TempDir()
	// config managing *.pdf
	if err := os.WriteFile(filepath.Join(root, "tailvault.toml"),
		[]byte("version = 1\n[storage]\nlocation = \"home-pi\"\n[rules]\nmin_size = \"5MB\"\ninclude = [\"**/*.pdf\"]\nauto_delete = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Version: 1, Storage: config.Storage{Location: "home-pi"},
		Rules: config.Rules{MinSize: "5MB", Include: []string{"**/*.pdf"}, AutoDelete: true}}

	// Working file is a clean POINTER (not the real bytes), recording the true size.
	ptr := "tailvault.v1\nsha256 " + contentSHA + "\nsize " + strconv.FormatInt(contentSize, 10) + "\nlocation home-pi\n"
	if err := os.WriteFile(filepath.Join(root, "a.pdf"), []byte(ptr), 0o644); err != nil {
		t.Fatal(err)
	}
	// Blob already on the node → dedup branch (no Put), so no real bytes needed.
	b := backend.NewFSBackend(t.TempDir())
	_ = b.Put(ctx, "objects/"+contentSHA, bytes.NewReader(content))

	lk := &lock.Lock{Version: 1}
	if _, err := Run(ctx, root, cfg, lk, deps(b), Options{}); err != nil {
		t.Fatalf("push: %v", err)
	}
	lk2, _ := lock.Load(filepath.Join(root, "tailvault.lock"))
	if len(lk2.Entries) != 1 {
		t.Fatalf("entries = %+v", lk2.Entries)
	}
	if lk2.Entries[0].Size != contentSize {
		t.Errorf("Entry.Size = %d, want content size %d (not pointer text size %d)",
			lk2.Entries[0].Size, contentSize, int64(len(ptr)))
	}
}

func TestPush_MoveRename_ZeroTransfer(t *testing.T) {
	ctx := context.Background()
	// tree has new.pdf; lock has old.pdf with the same sha; old.pdf gone.
	root, cfg := repo(t, map[string]string{"new.pdf": "shared"})
	b := backend.NewFSBackend(t.TempDir())
	_ = b.Put(ctx, "objects/"+sha([]byte("shared")), bytes.NewReader([]byte("shared")))
	puts := b.Puts
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{
		{Path: "old.pdf", SHA256: sha([]byte("shared")), Size: 6, Location: "home-pi", Pusher: "orig", PushedAt: time.Unix(1, 0).UTC()},
	}}

	res, err := Run(ctx, root, cfg, lk, deps(b), Options{})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if b.Puts != puts || len(res.Renamed) != 1 {
		t.Errorf("rename: Puts=%d (want %d), Renamed=%v", b.Puts, puts, res.Renamed)
	}
	lk2, _ := lock.Load(filepath.Join(root, "tailvault.lock"))
	if len(lk2.Entries) != 1 || lk2.Entries[0].Path != "new.pdf" || lk2.Entries[0].Pusher != "orig" {
		t.Errorf("renamed entry = %+v, want new.pdf carrying orig pusher", lk2.Entries)
	}
}

func TestPush_Deletion_MarksGC_UnlessPreserve(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{}) // empty tree
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{
		{Path: "d.pdf", SHA256: "sha-d", Size: 1, Location: "home-pi"},
		{Path: "keep.pdf", SHA256: "sha-k", Size: 1, Location: "home-pi", Preserve: true},
	}}

	res, err := Run(ctx, root, cfg, lk, deps(b), Options{})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(res.Dropped) != 2 {
		t.Errorf("dropped = %v, want both entries", res.Dropped)
	}
	if len(res.MarkedGC) != 1 || res.MarkedGC[0] != "sha-d" {
		t.Errorf("MarkedGC = %v, want only sha-d (keep.pdf preserved)", res.MarkedGC)
	}
	if b.Deletes != 0 {
		t.Errorf("Deletes = %d, want 0 (mark-only; sweep is task-16)", b.Deletes)
	}

	// The preserved blob must NOT be lost from the lock: push keeps a tombstone so
	// gc's keep/preserve set still references sha-k. d.pdf (auto-deleted) is gone.
	lk2, err := lock.Load(filepath.Join(root, "tailvault.lock"))
	if err != nil {
		t.Fatalf("lock load: %v", err)
	}
	if len(lk2.Entries) != 1 {
		t.Fatalf("lock entries = %+v, want only the keep.pdf tombstone", lk2.Entries)
	}
	tomb := lk2.Entries[0]
	if tomb.Path != "keep.pdf" || tomb.SHA256 != "sha-k" || !tomb.Deleted || !tomb.Preserve {
		t.Errorf("tombstone = %+v, want keep.pdf/sha-k Deleted&Preserve", tomb)
	}
}

// The end-to-end invariant the team-lead flagged: a preserved file's blob must
// survive gc after the file is deleted and pushed. Drive push, then run the gc
// keep/preserve planner over the resulting lock and assert the blob is not
// eligible for sweep.
func TestPush_DeletedPreserved_BlobSurvivesGC(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{}) // file already deleted from the tree
	b := backend.NewFSBackend(t.TempDir())
	// Seed the preserved blob on the node, as a prior push would have.
	if err := b.Put(ctx, "objects/sha-k", bytes.NewReader([]byte("kept"))); err != nil {
		t.Fatal(err)
	}
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{
		{Path: "keep.pdf", SHA256: "sha-k", Size: 4, Location: "home-pi", Preserve: true},
	}}

	if _, err := Run(ctx, root, cfg, lk, deps(b), Options{}); err != nil {
		t.Fatalf("push: %v", err)
	}
	lk2, err := lock.Load(filepath.Join(root, "tailvault.lock"))
	if err != nil {
		t.Fatalf("lock load: %v", err)
	}

	keep := gc.BuildKeepSet(map[string]*lock.Lock{"HEAD": lk2})
	pres := gc.BuildPreserveSet(map[string]*lock.Lock{"HEAD": lk2})
	plan := gc.PlanSweep([]string{"objects/sha-k"}, keep, pres)
	if len(plan.Eligible) != 0 {
		t.Errorf("plan.Eligible = %v, want empty — preserved blob must survive the sweep", plan.Eligible)
	}
}

// Auto-delete OFF: deleting a non-preserved file must NOT mark its blob for GC,
// and must keep a tombstone so the blob's keep-set reference survives (you opted
// out of reclamation). This is the complement-condition coverage.
func TestPush_Deletion_AutoDeleteOff_Tombstones(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{})
	cfg.Rules.AutoDelete = false
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{
		{Path: "d.pdf", SHA256: "sha-d", Size: 1, Location: "home-pi"}, // not preserved
	}}

	res, err := Run(ctx, root, cfg, lk, deps(b), Options{})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(res.MarkedGC) != 0 {
		t.Errorf("MarkedGC = %v, want none (auto_delete off keeps the blob)", res.MarkedGC)
	}
	lk2, err := lock.Load(filepath.Join(root, "tailvault.lock"))
	if err != nil {
		t.Fatalf("lock load: %v", err)
	}
	if len(lk2.Entries) != 1 || !lk2.Entries[0].Deleted {
		t.Errorf("entries = %+v, want a single d.pdf tombstone", lk2.Entries)
	}
}

// A tombstone persists across pushes: a second push with the file still absent
// carries the entry forward unchanged (and does not re-report it as Dropped).
func TestPush_Tombstone_PersistsAcrossPushes(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{})
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{
		{Path: "keep.pdf", SHA256: "sha-k", Size: 1, Location: "home-pi", Preserve: true},
	}}

	if _, err := Run(ctx, root, cfg, lk, deps(b), Options{}); err != nil {
		t.Fatalf("push #1: %v", err)
	}
	lk2, err := lock.Load(filepath.Join(root, "tailvault.lock"))
	if err != nil {
		t.Fatalf("lock load #1: %v", err)
	}

	res2, err := Run(ctx, root, cfg, lk2, deps(b), Options{})
	if err != nil {
		t.Fatalf("push #2: %v", err)
	}
	if len(res2.Dropped) != 0 {
		t.Errorf("push #2 Dropped = %v, want none (already tombstoned)", res2.Dropped)
	}
	lk3, err := lock.Load(filepath.Join(root, "tailvault.lock"))
	if err != nil {
		t.Fatalf("lock load #2: %v", err)
	}
	if len(lk3.Entries) != 1 || !lk3.Entries[0].Deleted || lk3.Entries[0].SHA256 != "sha-k" {
		t.Errorf("entries = %+v, want the keep.pdf tombstone preserved", lk3.Entries)
	}
}

// Resurrection: a file reappearing at a tombstoned path is re-pushed into a fresh
// LIVE entry (Deleted cleared), with no transfer when the blob is still present.
func TestPush_Tombstone_ResurrectedFile(t *testing.T) {
	ctx := context.Background()
	content := "resurrected"
	root, cfg := repo(t, map[string]string{"keep.pdf": content})
	b := backend.NewFSBackend(t.TempDir())
	// Blob already on the node (preserved through the deletion); tombstone in lock.
	if err := b.Put(ctx, "objects/"+sha([]byte(content)), bytes.NewReader([]byte(content))); err != nil {
		t.Fatal(err)
	}
	startPuts := b.Puts
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{
		{Path: "keep.pdf", SHA256: sha([]byte(content)), Size: int64(len(content)), Location: "home-pi", Preserve: true, Deleted: true},
	}}

	res, err := Run(ctx, root, cfg, lk, deps(b), Options{})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if b.Puts != startPuts {
		t.Errorf("Puts advanced by %d, want 0 (blob already present → dedup)", b.Puts-startPuts)
	}
	if len(res.MarkedGC) != 0 {
		t.Errorf("MarkedGC = %v, want none on resurrection", res.MarkedGC)
	}
	lk2, err := lock.Load(filepath.Join(root, "tailvault.lock"))
	if err != nil {
		t.Fatalf("lock load: %v", err)
	}
	if len(lk2.Entries) != 1 || lk2.Entries[0].Deleted {
		t.Errorf("entries = %+v, want a single LIVE keep.pdf entry (Deleted cleared)", lk2.Entries)
	}
}

func TestPush_NodeDown_AbortsZeroPutUnadvancedLock(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{"a.pdf": "alpha"})
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1}

	d := deps(b)
	d.Preflight = func(context.Context) error { return tserr.NodeOfflineErr("home-pi", errors.New("down")) }

	_, err := Run(ctx, root, cfg, lk, d, Options{})
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.NodeOffline {
		t.Fatalf("push: want TV-NODE-01, got %v", err)
	}
	if b.Puts != 0 {
		t.Errorf("Puts = %d on node-down, want 0", b.Puts)
	}
	if _, err := os.Stat(filepath.Join(root, "tailvault.lock")); !os.IsNotExist(err) {
		t.Errorf("lock should not be written on node-down preflight failure")
	}
}

func TestPush_WhoisFallbackToGit(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{"a.pdf": "alpha"})
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1}
	d := deps(b)
	d.Whois = func(context.Context) (string, error) { return "", errors.New("whois failed") }

	if _, err := Run(ctx, root, cfg, lk, d, Options{}); err != nil {
		t.Fatalf("push: %v", err)
	}
	lk2, _ := lock.Load(filepath.Join(root, "tailvault.lock"))
	if lk2.Entries[0].Pusher != "git@fallback" {
		t.Errorf("pusher = %q, want git@fallback", lk2.Entries[0].Pusher)
	}
}

func TestPush_DryRun_WritesNothing(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{"a.pdf": "alpha"})
	b := backend.NewFSBackend(t.TempDir())
	lk := &lock.Lock{Version: 1}

	res, err := Run(ctx, root, cfg, lk, deps(b), Options{DryRun: true})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(res.Uploaded) != 1 {
		t.Errorf("dry-run should still report the plan: uploaded=%v", res.Uploaded)
	}
	if b.Puts != 0 {
		t.Errorf("dry-run Puts = %d, want 0", b.Puts)
	}
	if _, err := os.Stat(filepath.Join(root, "tailvault.lock")); !os.IsNotExist(err) {
		t.Errorf("dry-run must not write the lock")
	}
}

func TestPush_PutVerifyFailure_NotRecorded(t *testing.T) {
	ctx := context.Background()
	root, cfg := repo(t, map[string]string{"a.pdf": "alpha"})
	lk := &lock.Lock{Version: 1}
	d := deps(&forgetfulBackend{})

	_, err := Run(ctx, root, cfg, lk, d, Options{})
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.ObjMissing {
		t.Fatalf("want TV-OBJ-01 on post-Put verify miss, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tailvault.lock")); !os.IsNotExist(err) {
		t.Errorf("lock must not be written when a blob fails post-Put verify")
	}
}

func TestPush_HistoryOn_AccumulatesVersions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := &config.Config{
		Version: 1,
		Storage: config.Storage{Location: "home-pi"},
		Rules:   config.Rules{MinSize: "5MB", Include: []string{"**/*.pdf"}, History: true, AutoDelete: true},
	}
	b := backend.NewFSBackend(t.TempDir())
	hpath := filepath.Join(root, "h.pdf")

	contents := []string{"v1-content", "v2-content", "v3-content"}
	lk := &lock.Lock{Version: 1}
	for _, c := range contents {
		if err := os.WriteFile(hpath, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := Run(ctx, root, cfg, lk, deps(b), Options{})
		if err != nil {
			t.Fatalf("push %q: %v", c, err)
		}
		// history-on never marks a superseded sha for GC.
		if len(res.MarkedGC) != 0 {
			t.Errorf("history-on push marked GC: %v", res.MarkedGC)
		}
		lk, err = lock.Load(filepath.Join(root, "tailvault.lock"))
		if err != nil {
			t.Fatalf("reload lock: %v", err)
		}
	}

	want := []string{sha([]byte("v3-content")), sha([]byte("v2-content")), sha([]byte("v1-content"))}

	// versions[] in the lock is newest-first with all three shas.
	e, ok := lk.Find("h.pdf")
	if !ok {
		t.Fatal("h.pdf missing from lock")
	}
	if !reflect.DeepEqual(e.Versions, want) {
		t.Errorf("lock versions = %v, want %v", e.Versions, want)
	}
	if e.SHA256 != want[0] {
		t.Errorf("current sha = %s, want newest %s", e.SHA256, want[0])
	}

	// refs/<path-id> on the node lists all three, newest-first.
	got, err := history.ReadVersions(ctx, b, history.PathID("h.pdf"))
	if err != nil {
		t.Fatalf("ReadVersions: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("refs versions = %v, want %v", got, want)
	}
}

// forgetfulBackend accepts Put but always reports the key absent — simulating a
// node that silently drops the write, which post-Put Stat verify must catch.
type forgetfulBackend struct{}

func (forgetfulBackend) Stat(context.Context, string) (backend.Meta, error) {
	return backend.Meta{Exists: false}, nil
}
func (forgetfulBackend) Get(context.Context, string, io.Writer) error          { return nil }
func (forgetfulBackend) Put(context.Context, string, io.Reader) error          { return nil }
func (forgetfulBackend) PutOverwrite(context.Context, string, io.Reader) error { return nil }
func (forgetfulBackend) Delete(context.Context, string) error                  { return nil }
func (forgetfulBackend) List(context.Context, string) ([]string, error)        { return nil, nil }
func (forgetfulBackend) HashObject(context.Context, string) (string, error) {
	return "", nil
}
