package fedtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/hooks"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
)

// DemoOpt configures a generated demo repo. The zero value is usable: MinSize
// defaults to "1KB" (test-sized so generated files cheaply straddle it).
type DemoOpt struct {
	MinSize string // tailvault.toml rules.min_size; default "1KB"
	History bool   // global history rule
}

// DemoRepo is a throwaway git working tree wired to a harness member: a real
// `git init` repo with tailvault.toml pointed at the member, the clean/smudge
// filter + lock merge driver registered, hooks installed, and generated files
// straddling min_size (some above, some below, in nested dirs). It is the bridge
// between the federation harness and the git-flow world — reused by Block 4's
// remote-command tests and the Block 7 dogfood demo (task-50).
type DemoRepo struct {
	Dir    string   // the repo working tree
	Member string   // the harness member this repo's storage points at
	Files  []string // generated repo-relative paths
	Big    []string // the subset above min_size (filter-managed)
	Small  []string // the subset below min_size (plain git)
}

// NewDemoRepo builds the fixture against member. The member is registered as a
// taildrive location (base_path = the member's vault root) in a per-test
// locations.toml so the CLI resolves the repo's storage to the harness member;
// XDG_CONFIG_HOME is pointed at the harness CacheDir when unset. Files are
// generated but left UNCOMMITTED (the consuming test drives track/add/commit).
func NewDemoRepo(t *testing.T, f *Fed, member string, opt DemoOpt) *DemoRepo {
	t.Helper()
	m := f.Member(t, member)

	if opt.MinSize == "" {
		opt.MinSize = "1KB"
	}
	if os.Getenv("XDG_CONFIG_HOME") == "" {
		t.Setenv("XDG_CONFIG_HOME", f.CacheDir)
	}

	// Register the member as a taildrive location so repo storage resolves to it.
	reg, err := locations.Load()
	if err != nil {
		t.Fatalf("fedtest: load locations: %v", err)
	}
	if err := reg.Add(member, locations.Location{
		Node: m.Node, BasePath: m.Root, Backend: locations.BackendTaildrive, Share: "vault",
	}); err != nil {
		t.Fatalf("fedtest: register location %q: %v", member, err)
	}
	if err := reg.Save(); err != nil {
		t.Fatalf("fedtest: save locations: %v", err)
	}

	dir := t.TempDir()
	demoGit(t, dir, "init")
	demoGit(t, dir, "config", "user.email", "demo@example.com")
	demoGit(t, dir, "config", "user.name", "demo")

	// tailvault.toml pointed at the member.
	cfg := config.Default()
	cfg.Storage.Location = member
	cfg.Rules.MinSize = opt.MinSize
	cfg.Rules.Include = []string{"**/*.bin"}
	cfg.Rules.History = opt.History
	cfg.Rules.AutoDelete = true
	if err := config.Write(filepath.Join(dir, "tailvault.toml"), &cfg); err != nil {
		t.Fatalf("fedtest: write tailvault.toml: %v", err)
	}

	// Register the clean/smudge filter + lock merge driver (mirrors `tailvault
	// init`) so the working tree behaves like a real tailvault repo.
	writeFile(t, filepath.Join(dir, ".gitattributes"), strings.Join([]string{
		"*.bin filter=tailvault -text",
		"tailvault.lock merge=tailvault",
		"",
	}, "\n"))
	for _, kv := range [][2]string{
		{"filter.tailvault.clean", "tailvault filter-clean %f"},
		{"filter.tailvault.smudge", "tailvault filter-smudge %f"},
		{"filter.tailvault.required", "true"},
		{"merge.tailvault.name", "tailvault lock per-path union merge"},
		{"merge.tailvault.driver", "tailvault __merge-lock %O %A %B"},
	} {
		demoGit(t, dir, "config", kv[0], kv[1])
	}
	if err := hooks.InstallHooks(dir, "tailvault"); err != nil {
		t.Fatalf("fedtest: install hooks: %v", err)
	}

	// Generate files straddling min_size: *.bin above (filter-managed), small
	// text below, across nested dirs.
	d := &DemoRepo{Dir: dir, Member: member}
	above := bytesAbove(opt.MinSize)
	gen := []struct {
		path string
		size int
		big  bool
	}{
		{"assets/board.bin", above, true},
		{"assets/nested/model.bin", above, true},
		{"notes/readme.txt", 64, false},
		{"notes/small.bin", 16, false}, // .bin but below min_size → plain git (size rule wins)
	}
	for _, g := range gen {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(g.path)), strings.Repeat("x", g.size))
		d.Files = append(d.Files, g.path)
		if g.big {
			d.Big = append(d.Big, g.path)
		} else {
			d.Small = append(d.Small, g.path)
		}
	}
	return d
}

// bytesAbove returns a byte count comfortably above the parsed min_size. The
// harness only needs files to STRADDLE a test-sized threshold, not be realistic,
// so for the common "1KB" default this is ~2KB; any other unit is treated as the
// 1KB default for sizing (the size rule itself is exercised by the rules package).
func bytesAbove(minSize string) int {
	switch strings.ToUpper(strings.TrimSpace(minSize)) {
	case "", "1KB", "1024":
		return 2048
	default:
		return 2048
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func demoGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("fedtest: git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
