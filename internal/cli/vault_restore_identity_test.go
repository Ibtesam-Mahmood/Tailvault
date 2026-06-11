package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
)

// restoreFixture builds a TAILDRIVE (local) location — gateLocation is a no-op
// there (SSH-only gating, DEV-46.8), so these tests exercise the restore logic
// without a password; SSH gating is covered by the auth package's own tests.
func restoreFixture(t *testing.T) (store, origID, receiptPath string) {
	t.Helper()
	store = t.TempDir()
	registerLocalLocation(t, "loc", store)

	g := identity.Genesis{
		ContentSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		OriginalPath:  "media/clip.mp4", IngestOpID: "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6", OriginNode: "home-pi",
	}
	origID, _ = identity.MintID(g)

	recDir := t.TempDir()
	if err := identity.WriteReceipt(recDir, identity.Receipt{
		ID: origID, Genesis: g, Path: "media/clip.mp4", SHA256AtPull: g.ContentSHA256,
		PulledAt: time.Unix(1700000000, 0).UTC(), SourceNode: "home-pi",
	}); err != nil {
		t.Fatal(err)
	}
	receiptPath = filepath.Join(recDir, origID+".toml")

	// Rebuilt catalog: same file/content, but a RE-MINTED id.
	cat := &catalog.Catalog{Version: catalog.SchemaVersion, VaultName: "loc", Node: "home-pi"}
	cat.Upsert(catalog.File{
		ID:     "1111111111111111111111111111111111111111111111111111111111111111",
		SHA256: g.ContentSHA256, Path: "media/clip.mp4", SyncMode: catalog.SyncModeManual,
	})
	catPath := filepath.Join(store, "meta", "catalog.toml")
	if err := os.MkdirAll(filepath.Dir(catPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := catalog.WriteAtomic(catPath, cat); err != nil {
		t.Fatal(err)
	}
	return store, origID, receiptPath
}

func TestVaultRestoreIdentityFromReceipt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, origID, receiptPath := restoreFixture(t)

	out, err := runVault(t, "vault", "restore-identity", "loc/media/clip.mp4", "--receipt", receiptPath, "--yes")
	if err != nil {
		t.Fatalf("restore-identity: %v\n%s", err, out)
	}
	cat, err := catalog.Load(filepath.Join(store, "meta", "catalog.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := cat.Find("media/clip.mp4"); f.ID != origID {
		t.Fatalf("id not restored: %s want %s", f.ID, origID)
	}
}

func TestVaultRestoreIdentityTamperedRecord(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, origID, receiptPath := restoreFixture(t)

	raw, _ := os.ReadFile(receiptPath)
	tampered := strings.Replace(string(raw), "home-pi", "evil-pi", 1)
	if err := os.WriteFile(receiptPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runVault(t, "vault", "restore-identity", "loc/media/clip.mp4", "--receipt", receiptPath, "--yes"); err == nil {
		t.Fatal("a non-self-certifying record must be rejected")
	}
	cat, _ := catalog.Load(filepath.Join(store, "meta", "catalog.toml"))
	if f, _ := cat.Find("media/clip.mp4"); f.ID == origID {
		t.Error("catalog must be untouched on a tampered record")
	}
}

func TestVaultRestoreIdentitySourceFlags(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, _, receiptPath := restoreFixture(t)
	if _, err := runVault(t, "vault", "restore-identity", "loc/media/clip.mp4", "--yes"); err == nil {
		t.Error("zero sources must error")
	}
	if _, err := runVault(t, "vault", "restore-identity", "loc/media/clip.mp4", "--receipt", receiptPath, "--record", receiptPath, "--yes"); err == nil {
		t.Error("two sources must error")
	}
}
