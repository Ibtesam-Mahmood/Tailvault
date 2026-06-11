package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
)

func TestVaultLs_Members(t *testing.T) {
	registerVault(t, "home-pi", []catalog.File{
		makeFile("30092d830e26", "media/a.pdf"),
		makeFile("abcdef123456", "docs/b.txt"),
	})
	out, err := run("vault", "ls")
	if err != nil {
		t.Fatalf("ls: %v\n%s", err, out)
	}
	for _, want := range []string{"home-pi", "online", "2 files", "1/1 members answered"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls members missing %q:\n%s", want, out)
		}
	}
}

func TestVaultLs_Entries(t *testing.T) {
	fa := makeFile("30092d830e26", "media/a.pdf")
	fb := makeFile("abcdef123456", "docs/b.txt")
	registerVault(t, "home-pi", []catalog.File{fa, fb})

	// Whole location.
	out, err := run("vault", "ls", "home-pi")
	if err != nil {
		t.Fatalf("ls loc: %v\n%s", err, out)
	}
	if !strings.Contains(out, "home-pi/media/a.pdf") || !strings.Contains(out, "home-pi/docs/b.txt") {
		t.Errorf("ls loc missing entries:\n%s", out)
	}

	// Path-filtered.
	out, err = run("vault", "ls", "home-pi/media")
	if err != nil {
		t.Fatalf("ls path: %v\n%s", err, out)
	}
	if !strings.Contains(out, "media/a.pdf") || strings.Contains(out, "docs/b.txt") {
		t.Errorf("ls path filter wrong:\n%s", out)
	}
}

func TestVaultLs_IDsOnly(t *testing.T) {
	fa := makeFile("30092d830e26", "media/a.pdf")
	registerVault(t, "home-pi", []catalog.File{fa})
	out, err := run("vault", "ls", "--ids-only", "home-pi")
	if err != nil {
		t.Fatalf("ls --ids-only: %v\n%s", err, out)
	}
	if !strings.Contains(out, identity.Short(fa.ID)) {
		t.Errorf("--ids-only missing short id:\n%s", out)
	}
}

func TestVaultLs_JSON(t *testing.T) {
	fa := makeFile("30092d830e26", "media/a.pdf")
	registerVault(t, "home-pi", []catalog.File{fa})
	out, err := run("vault", "ls", "--json", "home-pi")
	if err != nil {
		t.Fatalf("ls --json: %v\n%s", err, out)
	}
	var got lsJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(got.Entries) != 1 || got.Entries[0].ID != fa.ID {
		t.Errorf("json entries = %+v", got.Entries)
	}
	if len(got.Answered) != 1 {
		t.Errorf("members_answered = %v, want 1", got.Answered)
	}
}
