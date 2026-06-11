package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/gc"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// gcEligible reports whether the blob for sha would be collected by a federated
// gc of a single-member vault with no branch locks (i.e. nothing keeps it). It
// re-asserts the gc invariant that ONLY git-mode files are ever candidates.
func gcEligible(t *testing.T, dir, sha string) bool {
	t.Helper()
	ctx := context.Background()
	b := backend.NewTaildrive(dir)
	cat, err := catalog.Load(filepath.Join(dir, "meta", "catalog.toml"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	roster, err := fed.FromCatalog(cat)
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	fctx := &gc.FedContext{
		Backend: b,
		Roster:  roster,
		Probe:   func(context.Context, catalog.Member) error { return nil }, // all reachable
		Cat:     cat,
		Log:     &wal.Log{B: b},
		Actor:   "tester",
	}
	res, err := gc.PlanFederated(ctx, fctx, []string{"objects/" + sha}, gc.KeepSet{}, gc.KeepSet{})
	if err != nil {
		t.Fatalf("plan federated: %v", err)
	}
	for _, e := range res.Eligible {
		if e == sha {
			return true
		}
	}
	return false
}

func TestVaultSyncMode_ManualToGitRehashes(t *testing.T) {
	dirs := newFed(t, "home-pi")
	content := []byte("flip me to git\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})

	out, err := run("vault", "sync-mode", "home-pi/media/a.txt", "git")
	if err != nil {
		t.Fatalf("flip to git: %v\n%s", err, out)
	}
	nf, ok := mvReadCat(t, dirs["home-pi"]).Find("media/a.txt")
	if !ok {
		t.Fatal("entry vanished")
	}
	if nf.SyncMode != catalog.SyncModeGit {
		t.Errorf("sync_mode = %s, want git", nf.SyncMode)
	}
	if !nf.LastScanned.After(f.LastScanned) {
		t.Errorf("manual→git must stamp a fresh last_scanned: %s !> %s", nf.LastScanned, f.LastScanned)
	}
	// WAL: a done sync_mode op recording the flip.
	recs, _ := (&wal.Log{B: backend.NewTaildrive(dirs["home-pi"])}).Read(context.Background())
	found := false
	for _, r := range recs {
		if r.Entry.OpType == wal.OpSyncMode && r.State == wal.StateDone &&
			r.Entry.Args["from_mode"] == "manual" && r.Entry.Args["to_mode"] == "git" {
			found = true
		}
	}
	if !found {
		t.Error("missing done sync_mode WAL record manual→git")
	}
}

func TestVaultSyncMode_DriftedManualToGitReHomes(t *testing.T) {
	dirs := newFed(t, "home-pi")
	scanned := []byte("scanned\n")
	edited := []byte("edited since scan\n")
	// Recorded sha is sha256(scanned); the stored object is the edited bytes.
	f := realFile(t, dirs["home-pi"], "media/a.txt", scanned, edited, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})

	if _, err := run("vault", "sync-mode", "home-pi/media/a.txt", "git"); err != nil {
		t.Fatalf("flip drifted file to git: %v", err)
	}
	nf, _ := mvReadCat(t, dirs["home-pi"]).Find("media/a.txt")
	if nf.SHA256 == f.SHA256 {
		t.Error("a drifted manual file must adopt its true content hash on → git")
	}
	// Re-homed under the true hash so the entry stays content-addressed.
	if _, err := os.Stat(filepath.Join(dirs["home-pi"], "objects", nf.SHA256)); err != nil {
		t.Errorf("object must exist under the fresh hash objects/%s: %v", nf.SHA256, err)
	}
	// Projection-sufficiency (fix-35-D): the OpSyncMode record must carry the
	// re-hashed new_sha256 + last_scanned so heal's ProjectCatalog (which cannot
	// re-hash) replays the flip with the correct sha, not the stale one.
	recs, _ := (&wal.Log{B: backend.NewTaildrive(dirs["home-pi"])}).Read(context.Background())
	var sm *wal.Rec
	for i := range recs {
		if recs[i].Entry.OpType == wal.OpSyncMode {
			sm = &recs[i]
		}
	}
	if sm == nil {
		t.Fatal("no OpSyncMode record")
	}
	if sm.Entry.Args["new_sha256"] != nf.SHA256 || sm.Entry.Args["last_scanned"] == "" {
		t.Errorf("OpSyncMode record not projection-sufficient: %v (want new_sha256=%s + last_scanned)", sm.Entry.Args, nf.SHA256)
	}
}

func TestVaultSyncMode_GitToManualExemptsFromGC(t *testing.T) {
	dirs := newFed(t, "home-pi")
	content := []byte("gc candidate then exempt\n")
	f := realFile(t, dirs["home-pi"], "media/a.bin", content, content, catalog.SyncModeGit)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})

	// As a git-mode file kept by no lock, it IS a gc candidate.
	if !gcEligible(t, dirs["home-pi"], f.SHA256) {
		t.Fatal("a git-mode, unreferenced file should be gc-eligible before the flip")
	}

	out, err := run("vault", "sync-mode", "home-pi/media/a.bin", "manual")
	if err != nil {
		t.Fatalf("flip to manual: %v\n%s", err, out)
	}
	nf, _ := mvReadCat(t, dirs["home-pi"]).Find("media/a.bin")
	if nf.SyncMode != catalog.SyncModeManual {
		t.Errorf("sync_mode = %s, want manual", nf.SyncMode)
	}
	// After the flip, gc must skip the blob entirely (manual files are never
	// candidates, D14).
	if gcEligible(t, dirs["home-pi"], f.SHA256) {
		t.Error("a manual file must be gc-exempt after the flip")
	}
}

func TestVaultSyncMode_UnknownModeRejected(t *testing.T) {
	dirs := newFed(t, "home-pi")
	content := []byte("payload\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})

	if _, err := run("vault", "sync-mode", "home-pi/media/a.txt", "s3"); !isTVCode(err, tserr.ConfigBad) {
		t.Fatalf("unknown mode: want TV-CFG-01, got %v", err)
	}
	// Untouched.
	if nf, _ := mvReadCat(t, dirs["home-pi"]).Find("media/a.txt"); nf.SyncMode != catalog.SyncModeManual {
		t.Errorf("a rejected flip must leave sync_mode unchanged, got %s", nf.SyncMode)
	}
}

func TestVaultSyncMode_SameModeNoOp(t *testing.T) {
	dirs := newFed(t, "home-pi")
	content := []byte("payload\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})

	before, _ := os.ReadFile(filepath.Join(dirs["home-pi"], "meta", "catalog.toml"))
	if _, err := run("vault", "sync-mode", "home-pi/media/a.txt", "manual"); err != nil {
		t.Fatalf("same-mode no-op must succeed: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dirs["home-pi"], "meta", "catalog.toml"))
	if string(before) != string(after) {
		t.Error("a same-mode no-op must not rewrite the catalog")
	}
	// No WAL intent for a no-op.
	recs, _ := (&wal.Log{B: backend.NewTaildrive(dirs["home-pi"])}).Read(context.Background())
	for _, r := range recs {
		if r.Entry.OpType == wal.OpSyncMode {
			t.Error("a same-mode no-op must take no WAL intent")
		}
	}
}
