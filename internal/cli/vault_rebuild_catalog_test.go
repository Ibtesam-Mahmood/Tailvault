package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// initLocWithWAL bootstraps a local "loc" vault (real WAL + catalog) and returns
// the store root and the catalog path.
func initLocWithWAL(t *testing.T) (store, catPath string) {
	t.Helper()
	store = t.TempDir()
	writeStoreFile(t, store, "a.txt", "alpha")
	writeStoreFile(t, store, "sub/b.bin", "bravo-bytes")
	registerLocalLocation(t, "loc", store)
	if out, err := runVault(t, "vault", "init", "loc"); err != nil {
		t.Fatalf("vault init: %v\n%s", err, out)
	}
	return store, filepath.Join(store, "meta", "catalog.toml")
}

// TestRebuildCatalogMissingStandalone: a deleted catalog is reconstructed from
// the WAL with the same file list (ids/shas), proving the projection round-trips.
func TestRebuildCatalogMissingStandalone(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, catPath := initLocWithWAL(t)

	orig, err := catalog.Load(catPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(catPath); err != nil {
		t.Fatal(err)
	}

	out, err := runVault(t, "vault", "rebuild-catalog", "loc", "--standalone", "--vault-name", "loc", "--yes")
	if err != nil {
		t.Fatalf("rebuild-catalog: %v\n%s", err, out)
	}

	rebuilt, err := catalog.Load(catPath)
	if err != nil {
		t.Fatalf("rebuilt catalog not written: %v", err)
	}
	if len(rebuilt.Files) != len(orig.Files) {
		t.Fatalf("file count = %d, want %d", len(rebuilt.Files), len(orig.Files))
	}
	for _, of := range orig.Files {
		rf, ok := rebuilt.Find(of.Path)
		if !ok {
			t.Fatalf("rebuilt catalog missing %q", of.Path)
		}
		if rf.ID != of.ID || rf.SHA256 != of.SHA256 || rf.Size != of.Size {
			t.Errorf("file %q diverged: got %+v want id=%s sha=%s size=%d", of.Path, rf, of.ID, of.SHA256, of.Size)
		}
	}
	_ = store
}

// TestRebuildCatalogBrokenWALHardFails: a torn WAL chain must NEVER drive a
// rebuild — TV-FED-03, exit 6, and no catalog written.
func TestRebuildCatalogBrokenWALHardFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, catPath := initLocWithWAL(t)
	if err := os.Remove(catPath); err != nil {
		t.Fatal(err)
	}

	// Delete the genesis WAL entry (seq 0): the chain no longer anchors → broken.
	if err := os.Remove(filepath.Join(store, "meta", "wal", "000000000000.toml")); err != nil {
		t.Fatalf("could not find seq-0 WAL entry to corrupt: %v", err)
	}

	_, err := runVault(t, "vault", "rebuild-catalog", "loc", "--standalone", "--vault-name", "loc", "--yes")
	var te *tserr.Error
	if !errors.As(err, &te) || te.ExitCode() != 6 {
		t.Fatalf("want a TV-FED chain-broken error (exit 6), got %v", err)
	}
	if _, statErr := os.Stat(catPath); statErr == nil {
		t.Error("no catalog must be written when the WAL chain is broken")
	}
}

// TestRebuildCatalogMissingNoRosterRefuses: a missing catalog on an un-federated
// location WITHOUT --standalone refuses (never silently writes a federation-less
// catalog that could orphan a federated node).
func TestRebuildCatalogMissingNoRosterRefuses(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, catPath := initLocWithWAL(t)
	if err := os.Remove(catPath); err != nil {
		t.Fatal(err)
	}

	_, err := runVault(t, "vault", "rebuild-catalog", "loc", "--vault-name", "loc", "--yes")
	if err == nil {
		t.Fatal("rebuild must refuse when no roster can be recovered and --standalone is absent")
	}
	if _, statErr := os.Stat(catPath); statErr == nil {
		t.Error("no catalog must be written when the rebuild is refused")
	}
}

// TestRebuildCatalogDryRunWritesNothing: --dry-run reports but never writes.
func TestRebuildCatalogDryRunWritesNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, catPath := initLocWithWAL(t)
	if err := os.Remove(catPath); err != nil {
		t.Fatal(err)
	}

	out, err := runVault(t, "vault", "rebuild-catalog", "loc", "--standalone", "--vault-name", "loc", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(catPath); statErr == nil {
		t.Error("dry-run must not write the catalog")
	}
}

// TestRebuildCatalogPreservesFederationHeader: when the existing catalog is
// readable, its [federation] header rides through and the file list is reprojected
// from the WAL (here empty → an empty list), without needing --standalone.
func TestRebuildCatalogPreservesFederationHeader(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	registerLocalLocation(t, "loc", store)

	cat := &catalog.Catalog{
		Version: catalog.SchemaVersion, VaultName: "loc", Node: "home-pi",
		Federation: catalog.Federation{
			FedID:   "fed-1",
			Members: []catalog.Member{{Name: "loc", Node: "home-pi", Status: catalog.StatusActive}},
		},
	}
	// A bogus file row that the WAL projection (empty WAL) must drop.
	cat.Upsert(catalog.File{
		ID:     "3333333333333333333333333333333333333333333333333333333333333333",
		SHA256: "ff", Path: "stale.txt", SyncMode: catalog.SyncModeManual,
	})
	writeFedCatalog(t, store, cat)

	out, err := runVault(t, "vault", "rebuild-catalog", "loc", "--yes")
	if err != nil {
		t.Fatalf("rebuild-catalog: %v\n%s", err, out)
	}
	rebuilt, err := catalog.Load(filepath.Join(store, "meta", "catalog.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Federation.FedID != "fed-1" || len(rebuilt.Federation.Members) != 1 {
		t.Fatalf("federation header not preserved: %+v", rebuilt.Federation)
	}
	if len(rebuilt.Files) != 0 {
		t.Errorf("empty WAL must reproject to an empty file list, got %+v", rebuilt.Files)
	}
}
