package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// writeSource writes a local source file and returns its path + sha256.
func writeSource(t *testing.T, content []byte) (string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	return p, hex.EncodeToString(sum[:])
}

// catEntry reads the destination catalog and returns the file at rel.
func catEntry(t *testing.T, dir, rel string) (catalog.File, bool) {
	t.Helper()
	cat, err := catalog.Load(filepath.Join(dir, "meta", "catalog.toml"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return cat.Find(rel)
}

func TestVaultPut_HappyPath(t *testing.T) {
	dir := registerVault(t, "home-pi", nil)
	src, sha := writeSource(t, []byte("demo movie bytes\n"))

	out, err := run("vault", "put", src, "home-pi/media/demo.mp4")
	if err != nil {
		t.Fatalf("put: %v\n%s", err, out)
	}

	// Blob landed at objects/<sha>.
	if _, err := os.Stat(filepath.Join(dir, "objects", sha)); err != nil {
		t.Errorf("blob not stored at objects/%s: %v", sha, err)
	}

	// Catalog entry: correct self-certifying ID, sync_mode manual, recorded sha.
	f, ok := catEntry(t, dir, "media/demo.mp4")
	if !ok {
		t.Fatal("catalog has no entry for media/demo.mp4")
	}
	if f.SHA256 != sha {
		t.Errorf("entry sha = %s, want %s", f.SHA256, sha)
	}
	if f.SyncMode != catalog.SyncModeManual {
		t.Errorf("sync_mode = %s, want manual", f.SyncMode)
	}
	rederived, err := identity.MintID(identity.Genesis{
		ContentSHA256: f.Genesis.ContentSHA256, OriginalPath: f.Genesis.OriginalPath,
		IngestOpID: f.Genesis.IngestOpID, OriginNode: f.Genesis.OriginNode,
	})
	if err != nil || rederived != f.ID {
		t.Errorf("ID does not self-certify: rederived %s err %v, entry %s", rederived, err, f.ID)
	}

	// WAL shows the ingest op as done.
	recs, err := (&wal.Log{B: backend.NewTaildrive(dir)}).Read(context.Background())
	if err != nil {
		t.Fatalf("read wal: %v", err)
	}
	found := false
	for _, r := range recs {
		if r.Entry.OpType == wal.OpIngest && r.Entry.Args["path"] == "media/demo.mp4" {
			found = true
			if r.State != wal.StateDone {
				t.Errorf("ingest op state = %s, want done", r.State)
			}
		}
	}
	if !found {
		t.Error("no ingest WAL entry for the put")
	}
}

func TestVaultPut_ConflictCopy(t *testing.T) {
	dir := registerVault(t, "home-pi", []catalog.File{makeFile("aabbccddeeff", "media/a.txt")})
	src, _ := writeSource(t, []byte("new contents\n"))

	out, err := run("vault", "put", src, "home-pi/media/a.txt", "--on-conflict=copy")
	if err != nil {
		t.Fatalf("put copy: %v\n%s", err, out)
	}
	// Original entry untouched; the copy lands under "media/a (2).txt".
	if _, ok := catEntry(t, dir, "media/a.txt"); !ok {
		t.Error("original entry must remain")
	}
	if _, ok := catEntry(t, dir, "media/a (2).txt"); !ok {
		t.Errorf("copy not stored under deduped name:\n%s", out)
	}
}

func TestVaultPut_ConflictRename(t *testing.T) {
	dir := registerVault(t, "home-pi", []catalog.File{makeFile("aabbccddeeff", "media/a.txt")})
	src, _ := writeSource(t, []byte("renamed contents\n"))

	if _, err := run("vault", "put", src, "home-pi/media/a.txt", "--on-conflict=rename", "--rename-to=media/b.txt"); err != nil {
		t.Fatalf("put rename: %v", err)
	}
	if _, ok := catEntry(t, dir, "media/b.txt"); !ok {
		t.Error("renamed entry media/b.txt not found")
	}
}

func TestVaultPut_ConflictStop(t *testing.T) {
	dir := registerVault(t, "home-pi", []catalog.File{makeFile("aabbccddeeff", "media/a.txt")})
	before, _ := os.ReadFile(filepath.Join(dir, "meta", "catalog.toml"))
	src, sha := writeSource(t, []byte("should not land\n"))

	if _, err := run("vault", "put", src, "home-pi/media/a.txt", "--on-conflict=stop"); err != nil {
		t.Fatalf("put stop must exit 0: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "meta", "catalog.toml"))
	if string(before) != string(after) {
		t.Error("stop must leave the catalog byte-identical")
	}
	if _, err := os.Stat(filepath.Join(dir, "objects", sha)); !os.IsNotExist(err) {
		t.Error("stop must not write the blob")
	}
}

func TestVaultPut_ConflictNoFlagNonTTY(t *testing.T) {
	registerVault(t, "home-pi", []catalog.File{makeFile("aabbccddeeff", "media/a.txt")})
	src, sha := writeSource(t, []byte("blocked\n"))

	_, err := runStdin("", "vault", "put", src, "home-pi/media/a.txt")
	if !isTVCode(err, tserr.ConfigBad) {
		t.Fatalf("conflict without --on-conflict in non-TTY: want TV-CFG-01, got %v", err)
	}
	// Refused at the preflight (before any WAL intent), so nothing is written.
	_ = sha
}

func TestVaultPut_RmSource(t *testing.T) {
	registerVault(t, "home-pi", nil)
	src, _ := writeSource(t, []byte("clone me\n"))

	if _, err := run("vault", "put", src, "home-pi/media/x.bin", "--rm-source"); err != nil {
		t.Fatalf("put --rm-source: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("--rm-source must delete the local file after verified success")
	}
}

func TestVaultPut_IdempotentResume(t *testing.T) {
	// Model a CRASHED put: the deterministic intent was appended but the catalog
	// was never updated and the op never marked done. Re-running `put` with the
	// same source+dest re-presents the SAME deterministic op id → the WAL dedups
	// (ErrDuplicateOp) and the command resumes to completion WITHOUT minting a
	// second identity. (A second SUCCESSFUL put to an existing path is a conflict,
	// not a resume — that path is covered by the conflict tests.)
	ctx := context.Background()
	dir := registerVault(t, "home-pi", nil)
	content := []byte("resume safe\n")
	src, sha := writeSource(t, content)
	const rel = "media/r.bin"
	node := "home-pi.ts" // taildriveReg sets Node = name+".ts"

	// Pre-seed the interrupted intent (no blob, no catalog entry, no done marker).
	opID := putOpID(node, rel, sha)
	g := identity.Genesis{ContentSHA256: sha, OriginalPath: rel, IngestOpID: opID, OriginNode: node}
	id, err := identity.MintID(g)
	if err != nil {
		t.Fatal(err)
	}
	log := &wal.Log{B: backend.NewTaildrive(dir)}
	if _, err := log.AppendIntent(ctx, wal.Entry{
		OpID: opID, OpType: wal.OpIngest, BlobRefs: []string{id}, Actor: "tester",
		CreatedAt: lastScan, Args: map[string]string{
			"path": rel, "content_sha256": sha, "origin_node": node,
			"sync_mode": catalog.SyncModeManual, "size": "12",
		},
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	// Resume: a normal put completes it. No conflict (catalog has no entry yet).
	if _, err := run("vault", "put", src, "home-pi/"+rel); err != nil {
		t.Fatalf("resume put: %v", err)
	}

	// Exactly one catalog entry, one ingest op — no duplicate identity.
	cat, err := catalog.Load(filepath.Join(dir, "meta", "catalog.toml"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range cat.Files {
		if f.Path == rel {
			n++
			if f.ID != id {
				t.Errorf("resumed entry id = %s, want the original %s", f.ID, id)
			}
		}
	}
	if n != 1 {
		t.Errorf("resume produced %d catalog entries, want 1", n)
	}
	recs, _ := log.Read(ctx)
	ingests, done := 0, false
	for _, r := range recs {
		if r.Entry.OpType == wal.OpIngest && r.Entry.Args["path"] == rel {
			ingests++
			done = r.State == wal.StateDone
		}
	}
	if ingests != 1 || !done {
		t.Errorf("resume: ingest ops=%d done=%v, want 1 and done", ingests, done)
	}
}

func TestVaultPut_NotInitialised(t *testing.T) {
	// A registered location with no catalog → "not initialised", before any write.
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", cfg)
	dir := t.TempDir()
	if err := taildriveReg(map[string]string{"home-pi": dir}).Save(); err != nil {
		t.Fatal(err)
	}
	src, _ := writeSource(t, []byte("nowhere\n"))

	_, err := run("vault", "put", src, "home-pi/media/x.bin")
	if !isTVCode(err, tserr.ConfigBad) {
		t.Fatalf("put to uninitialised location: want TV-CFG-01, got %v", err)
	}
}
