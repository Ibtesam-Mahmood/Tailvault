package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
)

// seedLocal registers a local location named name at store, via the registry.
func seedLocal(t *testing.T, name, store string) {
	t.Helper()
	reg, err := locations.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(name, locations.Location{Backend: locations.BackendLocal, BasePath: store}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
}

func runRm(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"location", "rm"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestLocationRm_DoubleConfirm(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedLocal(t, "test", t.TempDir())

	out, err := runRm(t, "y\ny\n", "test")
	if err != nil {
		t.Fatalf("rm: %v", err)
	}
	if !strings.Contains(out, `removed location "test"`) {
		t.Errorf("out = %q", out)
	}
	reg, _ := locations.Load()
	if _, ok := reg.Locations["test"]; ok {
		t.Error("entry still present after rm")
	}
}

func TestLocationRm_AbortOnFirstNo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedLocal(t, "test", t.TempDir())

	out, err := runRm(t, "n\n", "test")
	if err != nil {
		t.Fatalf("rm: %v", err)
	}
	if !strings.Contains(out, "aborted") {
		t.Errorf("expected abort, out = %q", out)
	}
	reg, _ := locations.Load()
	if _, ok := reg.Locations["test"]; !ok {
		t.Error("entry removed despite first 'no'")
	}
}

func TestLocationRm_AbortOnSecondNo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedLocal(t, "test", t.TempDir())

	_, err := runRm(t, "y\nn\n", "test")
	if err != nil {
		t.Fatalf("rm: %v", err)
	}
	reg, _ := locations.Load()
	if _, ok := reg.Locations["test"]; !ok {
		t.Error("entry removed despite second 'no'")
	}
}

func TestLocationRm_NotRegistered(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := runRm(t, "y\ny\n", "ghost"); err == nil {
		t.Fatal("expected error for unknown location")
	}
}

func TestLocationRm_PurgeThirdConfirmDeletesData(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	// Plant store data + an unrelated file that must survive.
	if err := os.MkdirAll(filepath.Join(store, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "objects", "deadbeef"), []byte("blob"), 0o644); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(store, "keep.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedLocal(t, "test", store)

	out, err := runRm(t, "y\ny\ny\n", "test", "--purge")
	if err != nil {
		t.Fatalf("rm --purge: %v", err)
	}
	if !strings.Contains(out, "purged store data") {
		t.Errorf("out = %q", out)
	}
	if _, err := os.Stat(filepath.Join(store, "objects")); !os.IsNotExist(err) {
		t.Error("objects/ should be gone after purge")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("unrelated file keep.txt must survive purge: %v", err)
	}
}

func TestLocationRm_PurgeAbortsWithoutThird(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	if err := os.MkdirAll(filepath.Join(store, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedLocal(t, "test", store)

	// Two yeses then a NO at the purge confirm → nothing removed, data intact.
	if _, err := runRm(t, "y\ny\nn\n", "test", "--purge"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	reg, _ := locations.Load()
	if _, ok := reg.Locations["test"]; !ok {
		t.Error("entry removed despite purge 'no'")
	}
	if _, err := os.Stat(filepath.Join(store, "objects")); err != nil {
		t.Error("objects/ should remain when purge is declined")
	}
}

func TestLocationRm_NoNameResolvesCwd(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := t.TempDir()
	seedLocal(t, "here", store)
	t.Chdir(store)

	out, err := runRm(t, "y\ny\n") // no name → resolves to the cwd's location
	if err != nil {
		t.Fatalf("rm: %v", err)
	}
	if !strings.Contains(out, `removed location "here"`) {
		t.Errorf("out = %q", out)
	}
	reg, _ := locations.Load()
	if _, ok := reg.Locations["here"]; ok {
		t.Error("entry still present after no-name rm")
	}
}

func TestLocationRm_NoNameNoMatchErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	seedLocal(t, "elsewhere", t.TempDir()) // store is NOT the cwd
	t.Chdir(t.TempDir())                   // a different folder

	if _, err := runRm(t, "y\ny\n"); err == nil {
		t.Fatal("expected error when no location matches the current folder")
	}
}

func TestLocationRm_NoNamePurgeFromCwdDeletesAndHints(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	parent := t.TempDir()
	store := filepath.Join(parent, "store")
	if err := os.MkdirAll(filepath.Join(store, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedLocal(t, "here", store)
	t.Chdir(store)

	out, err := runRm(t, "y\ny\ny\n", "--purge") // no name, purge from cwd
	if err != nil {
		t.Fatalf("rm --purge: %v", err)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Error("store folder should be removed (it held only tailvault data)")
	}
	if !strings.Contains(out, "now deleted") || !strings.Contains(out, "cd ") {
		t.Errorf("expected a cd-to-parent hint, out = %q", out)
	}
}

func TestLocationRm_PurgeRejectedForRemote(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg, _ := locations.Load()
	_ = reg.Add("pi", locations.Location{Backend: locations.BackendSSH, Node: "pi.ts.net", BasePath: "/srv", User: "ibte"})
	_ = reg.Save()

	if _, err := runRm(t, "y\ny\ny\n", "pi", "--purge"); err == nil {
		t.Fatal("expected --purge to be rejected for a non-local backend")
	}
}
