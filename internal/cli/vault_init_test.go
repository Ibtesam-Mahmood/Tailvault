package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
)

// registerLocalLocation registers a taildrive-style location whose base_path is a
// local directory (locally accessible — the bootstrap-supported case).
func registerLocalLocation(t *testing.T, name, base string) {
	t.Helper()
	reg, err := locations.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(name, locations.Location{
		Node: "home-pi", BasePath: base, Backend: locations.BackendTaildrive, Share: "vault",
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
}

func writeStoreFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runVault(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestVaultInitBootstraps(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	writeStoreFile(t, store, "a.txt", "alpha")
	writeStoreFile(t, store, "sub/b.bin", "bravo-bytes")
	registerLocalLocation(t, "loc", store)

	outStr, err := runVault(t, "vault", "init", "loc")
	if err != nil {
		t.Fatalf("vault init: %v\n%s", err, outStr)
	}
	if !strings.Contains(outStr, "2 files tracked") {
		t.Errorf("output = %q", outStr)
	}

	cat, err := catalog.Load(filepath.Join(store, "meta", "catalog.toml"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if len(cat.Files) != 2 {
		t.Fatalf("want 2 files, got %d", len(cat.Files))
	}
	for _, f := range cat.Files {
		if f.SyncMode != catalog.SyncModeManual {
			t.Errorf("%s sync_mode = %q", f.Path, f.SyncMode)
		}
	}
}

func TestVaultInitDryRunNoWrites(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	writeStoreFile(t, store, "a.txt", "alpha")
	registerLocalLocation(t, "loc", store)

	outStr, err := runVault(t, "vault", "init", "loc", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !strings.Contains(outStr, "1 files to ingest") {
		t.Errorf("dry-run output = %q", outStr)
	}
	if _, err := os.Stat(filepath.Join(store, "meta", "catalog.toml")); !os.IsNotExist(err) {
		t.Error("--dry-run must not write a catalog")
	}
}

func TestVaultInitUnknownLocationIsConfigError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := runVault(t, "vault", "init", "nope")
	if err == nil {
		t.Fatal("want config error for unknown location")
	}
}

func TestVaultInitSSHUnsupported(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg, _ := locations.Load()
	_ = reg.Add("pi", locations.Location{Node: "pi", BasePath: "/mnt/x", Backend: locations.BackendSSH, User: "u"})
	_ = reg.Save()
	_, err := runVault(t, "vault", "init", "pi")
	if err == nil {
		t.Fatal("SSH bootstrap should be rejected (not yet supported)")
	}
	if !strings.Contains(err.Error(), "SSH remote bootstrap is not yet supported") {
		t.Errorf("error = %v", err)
	}
}

func TestVaultInitReRunNoOp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	writeStoreFile(t, store, "a.txt", "alpha")
	registerLocalLocation(t, "loc", store)

	if _, err := runVault(t, "vault", "init", "loc"); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(store, "meta", "catalog.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// Second run is idempotent.
	if _, err := runVault(t, "vault", "init", "loc"); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(store, "meta", "catalog.toml"))
	if string(first) != string(second) {
		t.Error("re-run changed the catalog")
	}
}
