package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
)

func TestVaultScanAddAndPrune(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	writeStoreFile(t, store, "a.txt", "alpha")
	writeStoreFile(t, store, "b.txt", "bravo")
	registerLocalLocation(t, "loc", store)

	if _, err := runVault(t, "vault", "init", "loc"); err != nil {
		t.Fatal(err)
	}

	// Manual drift: add c.txt, delete a.txt.
	writeStoreFile(t, store, "c.txt", "charlie")
	if err := os.Remove(filepath.Join(store, "a.txt")); err != nil {
		t.Fatal(err)
	}

	out, err := runVault(t, "vault", "scan", "loc", "--prune")
	if err != nil {
		t.Fatalf("vault scan: %v\n%s", err, out)
	}

	cat, err := catalog.Load(filepath.Join(store, "meta", "catalog.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cat.Find("a.txt"); ok {
		t.Error("a.txt should be pruned from the catalog")
	}
	if _, ok := cat.Find("c.txt"); !ok {
		t.Error("c.txt should be added to the catalog")
	}
	if _, ok := cat.Find("b.txt"); !ok {
		t.Error("b.txt should remain")
	}
}

func TestVaultScanDryRunNoWrites(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	writeStoreFile(t, store, "a.txt", "alpha")
	registerLocalLocation(t, "loc", store)
	if _, err := runVault(t, "vault", "init", "loc"); err != nil {
		t.Fatal(err)
	}
	writeStoreFile(t, store, "new.txt", "newborn")

	out, err := runVault(t, "vault", "scan", "loc", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 added") {
		t.Errorf("dry-run output = %q", out)
	}
	cat, _ := catalog.Load(filepath.Join(store, "meta", "catalog.toml"))
	if _, ok := cat.Find("new.txt"); ok {
		t.Error("--dry-run must not modify the catalog")
	}
}

func TestVaultScanNotBootstrapped(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	writeStoreFile(t, store, "a.txt", "alpha")
	registerLocalLocation(t, "loc", store)
	_, err := runVault(t, "vault", "scan", "loc")
	if err == nil || !strings.Contains(err.Error(), "not bootstrapped") {
		t.Fatalf("want not-bootstrapped config error, got %v", err)
	}
}
