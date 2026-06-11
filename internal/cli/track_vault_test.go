package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
)

func trackCatalog(t *testing.T, store string) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load(filepath.Join(store, "meta", "catalog.toml"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return c
}

func TestTrackVaultSingleFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	registerLocalLocation(t, "loc", store)
	writeStoreFile(t, store, "media/demo.mp4", "video-bytes")

	out, err := runVault(t, "track", "loc/media/demo.mp4")
	if err != nil {
		t.Fatalf("track: %v\n%s", err, out)
	}
	f, ok := trackCatalog(t, store).Find("media/demo.mp4")
	if !ok || f.SyncMode != catalog.SyncModeManual || len(f.ID) != 64 {
		t.Fatalf("not tracked correctly: %+v ok=%v", f, ok)
	}
}

func TestTrackVaultGlobRespectsIgnore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	registerLocalLocation(t, "loc", store)
	writeStoreFile(t, store, "media/a.mp4", "a")
	writeStoreFile(t, store, "media/b.mp4", "b")
	writeStoreFile(t, store, "media/scratch.tmp", "t")
	writeStoreFile(t, store, ".tailvaultignore", "*.tmp\n")

	if _, err := runVault(t, "track", "loc/media/**"); err != nil {
		t.Fatal(err)
	}
	cat := trackCatalog(t, store)
	if _, ok := cat.Find("media/a.mp4"); !ok {
		t.Error("a.mp4 should be tracked")
	}
	if _, ok := cat.Find("media/scratch.tmp"); ok {
		t.Error("glob must respect .tailvaultignore (*.tmp)")
	}
}

func TestTrackVaultExactOverridesIgnore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	registerLocalLocation(t, "loc", store)
	writeStoreFile(t, store, "secret.key", "k")
	writeStoreFile(t, store, ".tailvaultignore", "*.key\n")

	if _, err := runVault(t, "track", "loc/secret.key"); err != nil {
		t.Fatalf("track: %v", err)
	}
	if _, ok := trackCatalog(t, store).Find("secret.key"); !ok {
		t.Error("an explicit exact path must override .tailvaultignore (D22)")
	}
}

func TestTrackVaultIdempotent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	registerLocalLocation(t, "loc", store)
	writeStoreFile(t, store, "a.bin", "one")

	if _, err := runVault(t, "track", "loc/a.bin"); err != nil {
		t.Fatal(err)
	}
	id1, _ := trackCatalog(t, store).Find("a.bin")
	out, err := runVault(t, "track", "loc/a.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "already tracked") {
		t.Errorf("re-track should report already-tracked: %q", out)
	}
	id2, _ := trackCatalog(t, store).Find("a.bin")
	if id1.ID != id2.ID {
		t.Errorf("re-track re-minted identity: %s → %s", id1.ID, id2.ID)
	}
}

func TestTrackVaultRejectsReservedExactPath(t *testing.T) {
	// An exact path overrides .tailvaultignore (D22) but must NEVER let a user
	// register a vault-internal structural file (LOW 49.1). The guard fires before
	// any disk-presence check, so the path need not exist.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	registerLocalLocation(t, "loc", store)

	for _, p := range []string{"meta/catalog.toml", "objects/abc", "refs/heads/main", ".tailvaultignore"} {
		out, err := runVault(t, "track", "loc/"+p)
		if err == nil {
			t.Errorf("tracking reserved path %q must be refused, got success: %s", p, out)
		}
	}
}

func TestTrackVaultMissingPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	registerLocalLocation(t, "loc", store)
	if _, err := runVault(t, "track", "loc/nope.bin"); err == nil {
		t.Fatal("track of a missing path must error")
	}
}

func TestTrackRepoModeUnchangedForGlob(t *testing.T) {
	// An arg with no registered-location prefix stays repo-mode (Block 1). With no
	// repo present it should fail as a repo op (load tailvault.toml).
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := runVault(t, "track", "**/*.pdf")
	if err == nil || !strings.Contains(err.Error(), configFile) {
		t.Fatalf("repo-mode glob should attempt a repo op (load %s), got %v", configFile, err)
	}
}
