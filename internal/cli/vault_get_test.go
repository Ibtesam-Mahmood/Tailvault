package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// lastScan is a fixed last_scanned time so freshness messages are stable.
var lastScan = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// putObject writes raw bytes at dir/objects/<key> (the content-addressed store
// the taildrive backend reads). key need not equal sha256(content) — corruption
// tests deliberately store mismatched bytes under a recorded sha.
func putObject(t *testing.T, dir, key string, content []byte) {
	t.Helper()
	obj := filepath.Join(dir, "objects", key)
	if err := os.MkdirAll(filepath.Dir(obj), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(obj, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// realFile builds a catalog.File whose genesis SELF-CERTIFIES its id (so a pull
// receipt can be written) and whose recorded sha is sha256(content). storedBytes
// is what actually lands at objects/<sha> — pass the same bytes for a clean file,
// or different bytes to simulate corruption/drift while keeping the recorded sha.
func realFile(t *testing.T, dir, path string, content, storedBytes []byte, syncMode string) catalog.File {
	t.Helper()
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	putObject(t, dir, sha, storedBytes)

	g := identity.Genesis{ContentSHA256: sha, OriginalPath: path, IngestOpID: "op-1", OriginNode: "home-pi"}
	id, err := identity.MintID(g)
	if err != nil {
		t.Fatalf("mint id: %v", err)
	}
	return catalog.File{
		ID:          id,
		Genesis:     catalog.Genesis{ContentSHA256: sha, OriginalPath: path, IngestOpID: "op-1", OriginNode: "home-pi"},
		SHA256:      sha,
		Path:        path,
		SyncMode:    syncMode,
		Size:        int64(len(content)),
		CreatedAt:   lastScan,
		UpdatedAt:   lastScan,
		LastScanned: lastScan,
	}
}

func TestVaultGet_RoundTrip(t *testing.T) {
	content := []byte("the quick brown fox\n")
	dir := registerVault(t, "home-pi", nil) // register first; we rewrite the catalog with a real file
	f := realFile(t, dir, "media/a.txt", content, content, catalog.SyncModeManual)
	rewriteVault(t, dir, "home-pi", []catalog.File{f})

	dest := filepath.Join(t.TempDir(), "a.txt")
	out, err := run("vault", "get", "home-pi/media/a.txt", "-o", dest)
	if err != nil {
		t.Fatalf("get: %v\n%s", err, out)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(content) {
		t.Fatalf("delivered bytes mismatch: %q err=%v", got, err)
	}

	// Pull receipt exists and self-certifies (id == sha256(genesis)).
	recDir := filepath.Join(os.Getenv("HOME"), ".tailvault", "receipts")
	rec, err := identity.ReadReceipt(recDir, f.ID)
	if err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	if ok, _ := identity.Verify(rec.Genesis, rec.ID); !ok {
		t.Errorf("receipt genesis does not certify id %s", rec.ID)
	}
	if rec.SourceNode != "home-pi" || rec.SHA256AtPull != f.SHA256 {
		t.Errorf("receipt metadata = %+v", rec)
	}

	// Same file by short ID prefix.
	dest2 := filepath.Join(t.TempDir(), "b.txt")
	if _, err := run("vault", "get", identity.Short(f.ID), "-o", dest2); err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if b, _ := os.ReadFile(dest2); string(b) != string(content) {
		t.Errorf("get-by-id bytes mismatch: %q", b)
	}
}

func TestVaultGet_CorruptGitBlob(t *testing.T) {
	content := []byte("authentic git payload\n")
	dir := registerVault(t, "home-pi", nil)
	// Recorded sha is sha256(content), but the stored object is tampered bytes.
	f := realFile(t, dir, "media/a.bin", content, []byte("TAMPERED"), catalog.SyncModeGit)
	rewriteVault(t, dir, "home-pi", []catalog.File{f})

	destDir := t.TempDir()
	dest := filepath.Join(destDir, "a.bin")
	_, err := run("vault", "get", "home-pi/media/a.bin", "-o", dest)
	if !isTVCode(err, tserr.ObjMissing) {
		t.Fatalf("corrupt git blob: want TV-OBJ-01 (exit 5), got %v", err)
	}
	// No destination file and no stray temp file may survive a failed integrity check.
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("destination must not exist after integrity failure")
	}
	ents, _ := os.ReadDir(destDir)
	for _, e := range ents {
		t.Errorf("stray file left in dest dir: %s", e.Name())
	}
}

func TestVaultGet_UnknownSyncModeFailsClosed(t *testing.T) {
	// sync_mode is an open enum (D15): an unknown value (federation version skew)
	// must be treated content-addressed — a tampered blob hard-fails, never
	// delivered-and-labelled-"verified" (fix-42, never-silent-success).
	content := []byte("authentic payload\n")
	dir := registerVault(t, "home-pi", nil)
	f := realFile(t, dir, "media/a.bin", content, []byte("TAMPERED"), "future-mode-v3")
	rewriteVault(t, dir, "home-pi", []catalog.File{f})

	destDir := t.TempDir()
	dest := filepath.Join(destDir, "a.bin")
	_, err := run("vault", "get", "home-pi/media/a.bin", "-o", dest)
	if !isTVCode(err, tserr.ObjMissing) {
		t.Fatalf("unknown sync_mode + tampered blob: want TV-OBJ-01 (exit 5), got %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("no bytes may land for an unknown-mode integrity failure")
	}
	ents, _ := os.ReadDir(destDir)
	for _, e := range ents {
		t.Errorf("stray file left in dest dir: %s", e.Name())
	}
}

func TestVaultGet_EditedManualFile(t *testing.T) {
	content := []byte("scanned manual content\n")
	dir := registerVault(t, "home-pi", nil)
	// Manual file edited in place since last scan: stored bytes differ from the
	// recorded sha. This is legitimate drift (H12), not corruption.
	f := realFile(t, dir, "notes/m.txt", content, []byte("edited since scan\n"), catalog.SyncModeManual)
	rewriteVault(t, dir, "home-pi", []catalog.File{f})

	dest := filepath.Join(t.TempDir(), "m.txt")
	out, err := run("vault", "get", "home-pi/notes/m.txt", "-o", dest)
	if err != nil {
		t.Fatalf("manual drift must NOT error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "content has changed since last scan") || !strings.Contains(out, "2026-06-01") {
		t.Errorf("missing drift freshness notice with last_scanned:\n%s", out)
	}
	// The edited bytes are delivered — that is the truth the node holds.
	if b, _ := os.ReadFile(dest); string(b) != "edited since scan\n" {
		t.Errorf("manual file should deliver the node's current bytes, got %q", b)
	}
}

func TestVaultGet_RefuseOverwrite(t *testing.T) {
	content := []byte("payload\n")
	dir := registerVault(t, "home-pi", nil)
	f := realFile(t, dir, "media/a.txt", content, content, catalog.SyncModeManual)
	rewriteVault(t, dir, "home-pi", []catalog.File{f})

	dest := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(dest, []byte("pre-existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Without --force → refused (config error).
	if _, err := run("vault", "get", "home-pi/media/a.txt", "-o", dest); !isTVCode(err, tserr.ConfigBad) {
		t.Fatalf("overwrite without --force: want TV-CFG-01, got %v", err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "pre-existing" {
		t.Errorf("destination must be untouched when refused, got %q", b)
	}
	// With --force → succeeds.
	if _, err := run("vault", "get", "home-pi/media/a.txt", "-o", dest, "--force"); err != nil {
		t.Fatalf("get --force: %v", err)
	}
	if b, _ := os.ReadFile(dest); string(b) != string(content) {
		t.Errorf("--force should overwrite with fetched bytes, got %q", b)
	}
}

func TestVaultGet_ReceiptIdempotent(t *testing.T) {
	content := []byte("payload\n")
	dir := registerVault(t, "home-pi", nil)
	f := realFile(t, dir, "media/a.txt", content, content, catalog.SyncModeManual)
	rewriteVault(t, dir, "home-pi", []catalog.File{f})

	d := t.TempDir()
	for i := 0; i < 2; i++ {
		if _, err := run("vault", "get", "home-pi/media/a.txt", "-o", filepath.Join(d, "a.txt"), "--force"); err != nil {
			t.Fatalf("get #%d: %v", i, err)
		}
	}
	recDir := filepath.Join(os.Getenv("HOME"), ".tailvault", "receipts")
	if _, err := identity.ReadReceipt(recDir, f.ID); err != nil {
		t.Errorf("receipt missing after re-download: %v", err)
	}
}

func TestVaultGet_NoReceipt(t *testing.T) {
	content := []byte("payload\n")
	dir := registerVault(t, "home-pi", nil)
	f := realFile(t, dir, "media/a.txt", content, content, catalog.SyncModeManual)
	rewriteVault(t, dir, "home-pi", []catalog.File{f})

	dest := filepath.Join(t.TempDir(), "a.txt")
	out, err := run("vault", "get", "home-pi/media/a.txt", "-o", dest, "--no-receipt", "--json")
	if err != nil {
		t.Fatalf("get --no-receipt: %v\n%s", err, out)
	}
	var g getJSON
	if err := json.Unmarshal([]byte(out), &g); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if g.Receipt != "" {
		t.Errorf("--no-receipt must leave receipt empty, got %q", g.Receipt)
	}
	recDir := filepath.Join(os.Getenv("HOME"), ".tailvault", "receipts")
	if _, err := identity.ReadReceipt(recDir, f.ID); err == nil {
		t.Errorf("--no-receipt must not write a receipt file")
	}
}

// rewriteVault overwrites an already-registered member's catalog with files
// (registerVault wrote the federation membership; this swaps in real files whose
// blobs we placed under objects/).
func rewriteVault(t *testing.T, dir, member string, files []catalog.File) {
	t.Helper()
	members := []catalog.Member{{Name: member, Node: member + ".ts", Status: catalog.StatusActive}}
	writeMemberVault(t, dir, "fed-1", members, files)
}
