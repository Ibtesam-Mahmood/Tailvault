package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// registerVault writes a single-member federation to XDG_CONFIG_HOME and returns
// the member's vault dir. The member is registered as a taildrive location (local
// file IO), so the whole stat/ls path runs with no real node.
func registerVault(t *testing.T, member string, files []catalog.File) string {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", cfg) // sandbox ~/.tailvault/cache (vault ls snapshot) into the temp dir
	dir := t.TempDir()
	members := []catalog.Member{{Name: member, Node: member + ".ts", Status: catalog.StatusActive}}
	writeMemberVault(t, dir, "fed-1", members, files)
	if err := taildriveReg(map[string]string{member: dir}).Save(); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	return dir
}

func TestVaultStat_ByPath(t *testing.T) {
	f := makeFile("30092d830e26", "media/a.pdf")
	registerVault(t, "home-pi", []catalog.File{f})

	out, err := run("vault", "stat", "home-pi/media/a.pdf")
	if err != nil {
		t.Fatalf("stat: %v\n%s", err, out)
	}
	for _, want := range []string{identity.Short(f.ID), "home-pi/media/a.pdf", "manual", "1/1 answered"} {
		if !strings.Contains(out, want) {
			t.Errorf("stat output missing %q:\n%s", want, out)
		}
	}
}

func TestVaultStat_ByIDPrefix(t *testing.T) {
	f := makeFile("30092d830e26", "media/a.pdf")
	registerVault(t, "home-pi", []catalog.File{f})

	out, err := run("vault", "stat", "30092d830e26")
	if err != nil {
		t.Fatalf("stat by id: %v\n%s", err, out)
	}
	if !strings.Contains(out, "home-pi/media/a.pdf") {
		t.Errorf("stat-by-id missing path:\n%s", out)
	}
}

func TestVaultStat_JSON(t *testing.T) {
	f := makeFile("30092d830e26", "media/a.pdf")
	registerVault(t, "home-pi", []catalog.File{f})

	out, err := run("vault", "stat", "--json", "home-pi/media/a.pdf")
	if err != nil {
		t.Fatalf("stat --json: %v\n%s", err, out)
	}
	var got statJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if got.ID != f.ID || got.Home != "home-pi" || got.SyncMode != "manual" {
		t.Errorf("json = %+v", got)
	}
	if len(got.Answered) != 1 {
		t.Errorf("members_answered = %v, want 1", got.Answered)
	}
}

func TestVaultStat_Missing(t *testing.T) {
	registerVault(t, "home-pi", nil)
	_, err := run("vault", "stat", "home-pi/no/such.pdf")
	if !isTVCode(err, tserr.ObjMissing) {
		t.Errorf("missing file: want TV-OBJ-01, got %v", err)
	}
}
