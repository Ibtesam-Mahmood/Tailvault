package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// TestVaultChainBrokenIsTVFED03 wires F4/SG-3: a tampered WAL hash-chain must
// surface as TV-FED-03 (exit bucket 6) at the vault init/scan boundary, not a
// generic exit-1 error.
func TestVaultChainBrokenIsTVFED03(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	// Two files → two WAL entries (seq 0,1) so tampering seq 0 breaks seq 1's
	// prev_hash link (a lone genesis entry has no successor to validate it).
	writeStoreFile(t, store, "a.txt", "alpha")
	writeStoreFile(t, store, "b.txt", "bravo")
	registerLocalLocation(t, "loc", store)
	if _, err := runVault(t, "vault", "init", "loc"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Tamper seq-0's entry bytes while keeping it valid TOML (op_type value),
	// so it still decodes but its hash no longer matches seq-1's prev_hash.
	wal0 := filepath.Join(store, "meta", "wal", "000000000000.toml")
	raw, err := os.ReadFile(wal0)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(raw, []byte(`op_type = "ingest"`), []byte(`op_type = "ingesX"`), 1)
	if bytes.Equal(raw, tampered) {
		t.Fatal("tamper no-op: op_type token not found in seq-0 entry")
	}
	if err := os.WriteFile(wal0, tampered, 0o644); err != nil {
		t.Fatal(err)
	}

	assertTVFED03 := func(label string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected a chain-broken error", label)
		}
		var te *tserr.Error
		if !errors.As(err, &te) || te.Code != tserr.FedChainBroken {
			t.Fatalf("%s: want TV-FED-03, got %v", label, err)
		}
		if got := tserr.ExitCodeFor(err); got != 6 {
			t.Fatalf("%s: exit code = %d, want 6", label, got)
		}
	}

	// vault init re-reads the WAL up front → detects the break immediately.
	_, err = runVault(t, "vault", "init", "loc")
	assertTVFED03("vault init", err)

	// vault scan only touches the WAL when it has catch-up work to apply, so
	// introduce drift; Apply's AppendIntent then verifies (and rejects) the chain.
	writeStoreFile(t, store, "c.txt", "charlie")
	_, err = runVault(t, "vault", "scan", "loc", "--prune")
	assertTVFED03("vault scan", err)
}

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
