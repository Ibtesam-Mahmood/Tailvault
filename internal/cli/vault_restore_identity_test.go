package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
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

// fedRestoreFixture builds a TWO-member federation: "loc" (the rebuilt catalog,
// target of the restore) and "other". Both carry a [federation] section so the
// resolution engine fans out across them. If holdOnOther is true, "other"'s
// catalog already holds origID live — the collision the guard must catch.
func fedRestoreFixture(t *testing.T, holdOnOther bool) (storeLoc, origID, receiptPath string) {
	t.Helper()
	storeLoc, storeOther := t.TempDir(), t.TempDir()
	registerLocalLocation(t, "loc", storeLoc)
	registerLocalLocation(t, "other", storeOther)

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

	joined := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	members := []catalog.Member{
		{Name: "loc", Node: "home-pi", JoinedAt: joined, Status: catalog.StatusActive},
		{Name: "other", Node: "other-pi", JoinedAt: joined, Status: catalog.StatusActive},
	}
	fed := catalog.Federation{FedID: "fed-1", Members: members}

	// "loc": the rebuilt catalog — same file/content at the target path, RE-MINTED id.
	locCat := &catalog.Catalog{Version: catalog.SchemaVersion, VaultName: "loc", Node: "home-pi", Federation: fed}
	locCat.Upsert(catalog.File{
		ID:     "1111111111111111111111111111111111111111111111111111111111111111",
		SHA256: g.ContentSHA256, Path: "media/clip.mp4", SyncMode: catalog.SyncModeManual,
	})
	writeFedCatalog(t, storeLoc, locCat)

	// "other": its own catalog; holds origID live only when holdOnOther.
	otherCat := &catalog.Catalog{Version: catalog.SchemaVersion, VaultName: "other", Node: "other-pi", Federation: fed}
	if holdOnOther {
		otherCat.Upsert(catalog.File{ID: origID, SHA256: g.ContentSHA256, Path: "media/clip.mp4", SyncMode: catalog.SyncModeManual})
	}
	writeFedCatalog(t, storeOther, otherCat)

	return storeLoc, origID, receiptPath
}

func writeFedCatalog(t *testing.T, store string, cat *catalog.Catalog) {
	t.Helper()
	p := filepath.Join(store, "meta", "catalog.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := catalog.WriteAtomic(p, cat); err != nil {
		t.Fatal(err)
	}
}

// TestVaultRestoreIdentityFederationCollision is the spec-mandated guard (task-48
// line 127): the original id is still live on another member → hard-fail
// (TV-FED-04, exit 6) and the local catalog stays untouched.
func TestVaultRestoreIdentityFederationCollision(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	storeLoc, origID, receiptPath := fedRestoreFixture(t, true)

	_, err := runVault(t, "vault", "restore-identity", "loc/media/clip.mp4", "--receipt", receiptPath, "--yes")
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.FedIDCollision {
		t.Fatalf("want TV-FED-04 collision, got %v", err)
	}
	if te.ExitCode() != 6 {
		t.Errorf("collision exit = %d, want 6", te.ExitCode())
	}
	cat, _ := catalog.Load(filepath.Join(storeLoc, "meta", "catalog.toml"))
	if f, _ := cat.Find("media/clip.mp4"); f.ID == origID {
		t.Error("catalog must be untouched when a collision is detected")
	}
}

// TestVaultRestoreIdentityFederatedNoCollision: a federation IS present, but no
// member holds origID and all are reachable → resolution is Missing → restore
// proceeds normally.
func TestVaultRestoreIdentityFederatedNoCollision(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	storeLoc, origID, receiptPath := fedRestoreFixture(t, false)

	out, err := runVault(t, "vault", "restore-identity", "loc/media/clip.mp4", "--receipt", receiptPath, "--yes")
	if err != nil {
		t.Fatalf("restore-identity (no collision): %v\n%s", err, out)
	}
	cat, _ := catalog.Load(filepath.Join(storeLoc, "meta", "catalog.toml"))
	if f, _ := cat.Find("media/clip.mp4"); f.ID != origID {
		t.Fatalf("id not restored: %s want %s", f.ID, origID)
	}
}

// NOTE: the command-level PartialView→refuse property (member down → restore
// refused TV-FED-01/exit6) is deliberately NOT unit-tested here: a taildrive
// (local) member's reachability cannot be toggled in-process — Taildrive.Stat
// treats a missing file as data, not a node error, so a passive mount always
// reads "reachable". That property is covered by review-32 at the resolver level
// (stub probes) and is handed to task-50's down-member matrix (stub backends that
// can simulate unreachability) per the review-48 follow-up.

// TestVaultRestoreIdentityWrongTargetPath: a target path absent from the catalog
// is a clean error, not a panic (LOW 48.3).
func TestVaultRestoreIdentityWrongTargetPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, _, receiptPath := restoreFixture(t)
	if _, err := runVault(t, "vault", "restore-identity", "loc/does/not/exist.mp4", "--receipt", receiptPath, "--yes"); err == nil {
		t.Fatal("restore of a non-existent target path must error")
	}
}

// TestVaultRestoreIdentityContentMismatchWarns: a record whose original content
// sha matches neither the entry's current nor genesis sha emits a WARNING but
// still restores (manual files legitimately drift, H12 — advisory, not blocking).
func TestVaultRestoreIdentityContentMismatchWarns(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	registerLocalLocation(t, "loc", store)

	// A genesis whose content sha differs from the catalog entry's sha.
	g := identity.Genesis{
		ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OriginalPath:  "media/clip.mp4", IngestOpID: "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6", OriginNode: "home-pi",
	}
	origID, _ := identity.MintID(g)
	recDir := t.TempDir()
	if err := identity.WriteReceipt(recDir, identity.Receipt{
		ID: origID, Genesis: g, Path: "media/clip.mp4", SHA256AtPull: g.ContentSHA256,
		PulledAt: time.Unix(1700000000, 0).UTC(), SourceNode: "home-pi",
	}); err != nil {
		t.Fatal(err)
	}
	cat := &catalog.Catalog{Version: catalog.SchemaVersion, VaultName: "loc", Node: "home-pi"}
	cat.Upsert(catalog.File{
		ID:     "2222222222222222222222222222222222222222222222222222222222222222",
		SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Path:   "media/clip.mp4", SyncMode: catalog.SyncModeManual,
	})
	writeFedCatalog(t, store, cat)

	out, err := runVault(t, "vault", "restore-identity", "loc/media/clip.mp4", "--receipt", filepath.Join(recDir, origID+".toml"), "--yes")
	if err != nil {
		t.Fatalf("restore should proceed despite content mismatch: %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARNING") {
		t.Errorf("expected a content-mismatch WARNING, got %q", out)
	}
	loaded, _ := catalog.Load(filepath.Join(store, "meta", "catalog.toml"))
	if f, _ := loaded.Find("media/clip.mp4"); f.ID != origID {
		t.Errorf("restore should still apply: id %s want %s", f.ID, origID)
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
