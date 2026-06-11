package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// mvDeleteRecs returns the OpDelete WAL records on a member.
func mvDeleteRecs(t *testing.T, dir string) []wal.Rec {
	t.Helper()
	recs, err := (&wal.Log{B: backend.NewTaildrive(dir)}).Read(context.Background())
	if err != nil {
		t.Fatalf("read wal: %v", err)
	}
	var out []wal.Rec
	for _, r := range recs {
		if r.Entry.OpType == wal.OpDelete {
			out = append(out, r)
		}
	}
	return out
}

func TestVaultRm_HappyPath(t *testing.T) {
	dirs := newFed(t, "home-pi")
	content := []byte("delete me\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})

	out, err := run("vault", "rm", "home-pi/media/a.txt", "--yes")
	if err != nil {
		t.Fatalf("rm: %v\n%s", err, out)
	}
	// Blob gone, catalog entry gone.
	if _, err := os.Stat(filepath.Join(dirs["home-pi"], "objects", f.SHA256)); !os.IsNotExist(err) {
		t.Error("blob must be deleted")
	}
	if _, ok := mvReadCat(t, dirs["home-pi"]).Find("media/a.txt"); ok {
		t.Error("catalog entry must be gone")
	}
	// WAL: a done delete carrying the deleted identity (the last audit trace).
	recs := mvDeleteRecs(t, dirs["home-pi"])
	if len(recs) != 1 || recs[0].State != wal.StateDone {
		t.Fatalf("want 1 done delete, got %+v", recs)
	}
	if recs[0].Entry.Args["id"] != f.ID || recs[0].Entry.Args["content_sha256"] != f.SHA256 {
		t.Errorf("delete record must carry id+sha: %v", recs[0].Entry.Args)
	}
}

func TestVaultRm_SharedBlob(t *testing.T) {
	// Two entries with the same sha: the first rm keeps the blob, the second
	// removes it (content-addressed last-referent rule).
	dirs := newFed(t, "home-pi")
	content := []byte("shared bytes\n")
	a := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	b := a // same sha/id head; give it a distinct id + path
	b.Path = "media/b.txt"
	b.ID = strings.Repeat("b", 64)
	b.Genesis.OriginalPath = "media/b.txt"
	writeFedVault(t, dirs, "home-pi", []catalog.File{a, b})

	if _, err := run("vault", "rm", "home-pi/media/a.txt", "--yes"); err != nil {
		t.Fatalf("first rm: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirs["home-pi"], "objects", a.SHA256)); err != nil {
		t.Error("blob must survive while a second entry still references it")
	}
	if _, err := run("vault", "rm", "home-pi/media/b.txt", "--yes"); err != nil {
		t.Fatalf("second rm: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirs["home-pi"], "objects", a.SHA256)); !os.IsNotExist(err) {
		t.Error("blob must be deleted once the last referent is gone")
	}
}

func TestVaultRm_MovedFileDeletesAtNewHome(t *testing.T) {
	// rm by id of a file that has moved targets the LIVE home; the source's
	// moved_to forwarder stub is left untouched (journal gc owns stub cleanup).
	dirs := newFed(t, "home-pi", "office-nas")
	content := []byte("relocate then delete\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})
	writeFedVault(t, dirs, "office-nas", nil)

	if _, err := run("vault", "mv", "home-pi/media/a.txt", "office-nas/media/a.txt"); err != nil {
		t.Fatalf("setup move: %v", err)
	}
	// rm by id → resolves to office-nas (current home) and deletes there.
	if _, err := run("vault", "rm", f.ID, "--yes"); err != nil {
		t.Fatalf("rm moved file: %v", err)
	}
	if _, ok := mvReadCat(t, dirs["office-nas"]).Find("media/a.txt"); ok {
		t.Error("dest entry must be deleted")
	}
	if _, err := os.Stat(filepath.Join(dirs["office-nas"], "objects", f.SHA256)); !os.IsNotExist(err) {
		t.Error("blob at the live home must be deleted")
	}
	// The source's moved_to forwarder record is left in place.
	src := mvMoveRecs(t, dirs["home-pi"])
	if len(src) != 1 || src[0].Entry.Args["moved_to"] != "office-nas" {
		t.Errorf("source forwarder must survive an rm at the new home: %+v", src)
	}
}

func TestVaultRm_GitModeWarning(t *testing.T) {
	dirs := newFed(t, "home-pi")
	content := []byte("git payload\n")
	f := realFile(t, dirs["home-pi"], "media/a.bin", content, content, catalog.SyncModeGit)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})

	out, err := run("vault", "rm", "home-pi/media/a.bin", "--yes")
	if err != nil {
		t.Fatalf("rm git file: %v\n%s", err, out)
	}
	if !strings.Contains(out, "git-mode file") || !strings.Contains(out, "hard-fail") {
		t.Errorf("git-mode rm must emit the loud warning:\n%s", out)
	}
}

func TestVaultRm_NonTTYWithoutYes(t *testing.T) {
	dirs := newFed(t, "home-pi")
	content := []byte("keep me\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})

	_, err := runStdin("", "vault", "rm", "home-pi/media/a.txt")
	if !isTVCode(err, tserr.ConfigBad) {
		t.Fatalf("rm in non-TTY without --yes: want TV-CFG-01, got %v", err)
	}
	// Nothing touched.
	if _, ok := mvReadCat(t, dirs["home-pi"]).Find("media/a.txt"); !ok {
		t.Error("a refused rm must leave the entry in place")
	}
	if _, err := os.Stat(filepath.Join(dirs["home-pi"], "objects", f.SHA256)); err != nil {
		t.Error("a refused rm must leave the blob in place")
	}
	if len(mvDeleteRecs(t, dirs["home-pi"])) != 0 {
		t.Error("a refused rm must take no WAL intent")
	}
}

func TestVaultRm_JSONCarriesGenesis(t *testing.T) {
	dirs := newFed(t, "home-pi")
	content := []byte("payload\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})

	out, err := run("vault", "rm", "home-pi/media/a.txt", "--yes", "--json")
	if err != nil {
		t.Fatalf("rm json: %v\n%s", err, out)
	}
	var r rmJSON
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if r.Genesis != f.Genesis || !r.BlobDeleted || r.ID != f.ID {
		t.Errorf("json = %+v; want genesis carried, blob deleted, id %s", r, f.ID)
	}
}

func TestVaultRm_IdempotentResume(t *testing.T) {
	// A crashed rm: the deterministic intent exists but the blob/catalog were not
	// touched. Re-running re-presents the same op id → WAL dedups → completes with
	// no duplicate delete record.
	ctx := context.Background()
	dirs := newFed(t, "home-pi")
	content := []byte("resume delete\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})

	opID := rmOpID(f.ID, f.SHA256)
	log := &wal.Log{B: backend.NewTaildrive(dirs["home-pi"])}
	if _, err := log.AppendIntent(ctx, wal.Entry{
		OpID: opID, OpType: wal.OpDelete, BlobRefs: []string{f.ID}, Actor: "crashed",
		CreatedAt: lastScan, Args: map[string]string{"id": f.ID, "path": "media/a.txt", "content_sha256": f.SHA256},
	}); err != nil {
		t.Fatalf("seed interrupted intent: %v", err)
	}

	if _, err := run("vault", "rm", "home-pi/media/a.txt", "--yes"); err != nil {
		t.Fatalf("resume rm: %v", err)
	}
	recs := mvDeleteRecs(t, dirs["home-pi"])
	if len(recs) != 1 || recs[0].State != wal.StateDone || recs[0].Entry.OpID != opID {
		t.Fatalf("resume produced %+v; want 1 done record with the original op id", recs)
	}
	if _, err := os.Stat(filepath.Join(dirs["home-pi"], "objects", f.SHA256)); !os.IsNotExist(err) {
		t.Error("resume must complete the blob delete")
	}
}
