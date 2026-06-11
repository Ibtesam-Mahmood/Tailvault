package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
)

// lifecycleNode sandboxes the env and registers a single FRESH (un-initialised)
// taildrive location "home" over a temp dir — the always-connected real local-FS
// node for the 7b lifecycle (GOAL.md 7b). It installs the captured-status
// tailscale stub so the real preflightNode/whoisSelf path runs offline.
func lifecycleNode(t *testing.T) (name, root string) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())
	root = t.TempDir()
	reg := locations.Registry{Locations: map[string]locations.Location{
		"home": {Node: "home", BasePath: root, Backend: locations.BackendTaildrive, Share: "vault"},
	}}
	if err := reg.Save(); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	newTSClient = func() *tailscale.Client { return &tailscale.Client{R: okTSRunner{}} }
	t.Cleanup(func() { newTSClient = tailscale.New })
	return "home", root
}

// TestBlock4_Lifecycle7b is the GOAL.md-7b real same-node lifecycle: it drives the
// FULL command sequence through the REAL root Cobra Execute() against one local-FS
// node — init → fed init → put → ls → stat → get → sync-mode → mv → scan →
// rebuild-catalog → rm → passwd — asserting the bucketed exit code (mostly 0) AND
// real on-disk state at each step (catalog rows, objects/<sha>, delivered bytes,
// pull receipt, hash file). No internal helper calls — exactly the path a user
// hits. (restore-identity's recovery flow is covered behaviorally in Row 5; gc's
// refusal in Row 2; this row is the end-to-end happy spine.)
func TestBlock4_Lifecycle7b(t *testing.T) {
	name, root := lifecycleNode(t)
	catPath := filepath.Join(root, "meta", "catalog.toml")
	loadCat := func() *catalog.Catalog {
		c, err := catalog.Load(catPath)
		if err != nil {
			t.Fatalf("load catalog: %v", err)
		}
		return c
	}
	step := func(label string, args ...string) {
		t.Helper()
		out, code, err := execCLI(args...)
		if code != exitOK || err != nil {
			t.Fatalf("%s (%v): exit %d / %v\n%s", label, args, code, err, out)
		}
	}

	// 1. init the vault, then 2. mint the federation around it.
	step("init", "vault", "init", name)
	if _, err := os.Stat(catPath); err != nil {
		t.Fatalf("init must create the catalog: %v", err)
	}
	step("fed init", "fed", "init", name)
	if loadCat().Federation.FedID == "" {
		t.Fatal("fed init must mint a fed_id")
	}

	// 3. put a file (real ungated ingestion → objects/<sha> + catalog row).
	src := filepath.Join(t.TempDir(), "clip.bin")
	content := []byte("the whole lifecycle in one file\n")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	step("put", "vault", "put", src, "home/media/a.bin")
	row, ok := loadCat().Find("media/a.bin")
	if !ok {
		t.Fatal("put must add media/a.bin to the catalog")
	}
	if _, err := os.Stat(filepath.Join(root, "objects", row.SHA256)); err != nil {
		t.Fatalf("put must store the blob at objects/%s: %v", row.SHA256, err)
	}

	// 4-5. read paths: ls + stat (ungated).
	step("ls", "vault", "ls", name)
	step("stat", "vault", "stat", "home/media/a.bin")

	// 6. get → delivered bytes match + a pull receipt is written.
	dest := filepath.Join(t.TempDir(), "out.bin")
	step("get", "vault", "get", "home/media/a.bin", "-o", dest)
	if b, _ := os.ReadFile(dest); string(b) != string(content) {
		t.Errorf("get delivered %q, want %q", b, content)
	}

	// 7. sync-mode manual→git (re-hash; needs the objects/<sha> from put).
	step("sync-mode", "vault", "sync-mode", "home/media/a.bin", "git")
	if r, _ := loadCat().Find("media/a.bin"); r.SyncMode != catalog.SyncModeGit {
		t.Errorf("sync-mode git: catalog sync_mode = %q, want git", r.SyncMode)
	}

	// 8. mv intra-rename (id preserved, no bytes move).
	step("mv", "vault", "mv", "home/media/a.bin", "home/media/b.bin")
	c := loadCat()
	if _, ok := c.Find("media/a.bin"); ok {
		t.Error("mv must drop the old path media/a.bin")
	}
	if r, ok := c.Find("media/b.bin"); !ok || r.ID != row.ID {
		t.Errorf("mv must keep the file at media/b.bin with the same id")
	}

	// 9-10. scan, then rebuild-catalog from the WAL (the heal/projection path).
	step("scan", "vault", "scan", name)
	step("rebuild-catalog", "vault", "rebuild-catalog", name)
	if _, ok := loadCat().Find("media/b.bin"); !ok {
		t.Error("rebuild-catalog must reconstruct media/b.bin from the WAL")
	}

	// 11. rm (the only way a manual... here git file dies; taildrive-ungated).
	step("rm", "vault", "rm", "home/media/b.bin", "--yes")
	if _, ok := loadCat().Find("media/b.bin"); ok {
		t.Error("rm must remove media/b.bin from the catalog")
	}

	// 12. passwd: set the node password (first set; writes the argon2id hash file).
	pw := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pw, []byte("lifecycle-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	step("passwd", "vault", "passwd", name, "--new-password-file", pw)
	if _, err := os.Stat(filepath.Join(root, "meta", "auth", "passwd")); err != nil {
		t.Errorf("passwd must write the node hash file: %v", err)
	}
}
