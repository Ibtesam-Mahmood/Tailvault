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
	"github.com/Ibtesam-Mahmood/tailvault/internal/fedtest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/ingest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// putThenFlip seeds a manual file through the REAL `vault put` path (which stores
// the content-addressed blob at objects/<sha> so a later manual→git re-hash has
// something to hash), then drives `vault sync-mode … git` through the real CLI.
// Returns the member root.
func putThenFlip(t *testing.T, f *fedtest.Fed, content []byte) string {
	t.Helper()
	home := f.Member(t, "home").Root
	src := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code, err := execCLI("vault", "put", src, "home/media/a.bin"); code != exitOK || err != nil {
		t.Fatalf("put (ungated ingestion) must succeed: exit %d / %v\n%s", code, err, out)
	}
	return home
}

func rebuiltRow(t *testing.T, home, path string) catalog.File {
	t.Helper()
	live, err := catalog.Load(filepath.Join(home, "meta", "catalog.toml"))
	if err != nil {
		t.Fatalf("load live catalog: %v", err)
	}
	recs, err := (&wal.Log{B: backend.NewTaildrive(home)}).Read(context.Background())
	if err != nil {
		t.Fatalf("read wal: %v", err)
	}
	rebuilt, err := ingest.ProjectCatalog(live, recs, "home")
	if err != nil {
		t.Fatalf("project catalog: %v", err)
	}
	row, ok := rebuilt.Find(path)
	if !ok {
		t.Fatalf("rebuilt catalog missing %s", path)
	}
	return row
}

// TestBlock4_SyncModeFlipRebuildRoundTrip (review-50 row 4): a manual→git flip via
// the real CLI must replay from the WAL alone (ProjectCatalog) as git with the
// flip's recorded sha and a stamped last_scanned — the OpSyncMode record must
// carry to_mode + new_sha256 + last_scanned (else a rebuild keeps a stale
// mode/sha: gc/verify integrity hazard).
func TestBlock4_SyncModeFlipRebuildRoundTrip(t *testing.T) {
	f := fedtest.New(t, "home")
	content := []byte("flip me to git\n")
	home := putThenFlip(t, f, content)

	if _, code, err := execCLI("vault", "sync-mode", "home/media/a.bin", "git"); code != exitOK || err != nil {
		t.Fatalf("sync-mode git: exit %d / %v", code, err)
	}

	// Live state is git; rebuilt-from-WAL matches it (mode + sha + scan stamped).
	live, _ := catalog.Load(filepath.Join(home, "meta", "catalog.toml"))
	lr, _ := live.Find("media/a.bin")
	if lr.SyncMode != catalog.SyncModeGit {
		t.Fatalf("live sync_mode = %q, want git", lr.SyncMode)
	}
	rb := rebuiltRow(t, home, "media/a.bin")
	if rb.SyncMode != catalog.SyncModeGit {
		t.Errorf("rebuilt sync_mode = %q, want git (OpSyncMode to_mode must project)", rb.SyncMode)
	}
	if rb.SHA256 != lr.SHA256 {
		t.Errorf("rebuilt sha = %s, want %s (new_sha256 from the flip op)", rb.SHA256, lr.SHA256)
	}
	if rb.LastScanned.IsZero() {
		t.Error("rebuilt last_scanned must be stamped from the flip op (a fresh git file is a scan)")
	}
	if rb.ID != lr.ID {
		t.Errorf("rebuilt id = %s, want %s (flip preserves identity)", rb.ID, lr.ID)
	}
}

// TestBlock4_SyncModeFlipDriftRebuild (review-50 row 4, drift case): a manual file
// edited in place since its scan (stored blob != recorded sha) that is flipped to
// git adopts its TRUE content hash; the rebuild must reflect that fresh sha, not
// the stale one. (Tampering the stored blob is a legitimate fault-injection — H12
// drift — exercising the re-hash + re-home on flip.)
func TestBlock4_SyncModeFlipDriftRebuild(t *testing.T) {
	f := fedtest.New(t, "home")
	home := putThenFlip(t, f, []byte("scanned content\n"))

	// Drift: overwrite the stored blob with edited bytes (its recorded sha now
	// disagrees with the on-disk content).
	live0, _ := catalog.Load(filepath.Join(home, "meta", "catalog.toml"))
	orig, _ := live0.Find("media/a.bin")
	edited := []byte("edited in place since the scan\n")
	if err := os.WriteFile(filepath.Join(home, "objects", orig.SHA256), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(edited)
	driftSHA := hex.EncodeToString(sum[:])

	if _, code, err := execCLI("vault", "sync-mode", "home/media/a.bin", "git"); code != exitOK || err != nil {
		t.Fatalf("sync-mode git (drift): exit %d / %v", code, err)
	}

	// The flip adopted the true (drift) hash and re-homed the blob under it.
	if _, err := os.Stat(filepath.Join(home, "objects", driftSHA)); err != nil {
		t.Errorf("drifted blob must be re-homed under its true hash objects/%s: %v", driftSHA, err)
	}
	rb := rebuiltRow(t, home, "media/a.bin")
	if rb.SyncMode != catalog.SyncModeGit || rb.SHA256 != driftSHA {
		t.Errorf("rebuilt = {mode:%s sha:%s}, want {git, %s} (drift re-hash must project, not the stale sha)", rb.SyncMode, rb.SHA256, driftSHA)
	}
}
