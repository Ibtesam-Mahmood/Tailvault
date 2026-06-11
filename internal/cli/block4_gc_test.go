package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fedtest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// TestBlock4_GCRefusal is task-50 Row 2: the BEHAVIORAL proof that remote gc is
// password-gated (fix-46.A / SG-9). It drives the REAL `tailvault gc` command
// (repo-side, federated path) against a password-PROTECTED member with an
// unreferenced (gc-eligible) git-mode blob and asserts, by REAL bucketed exit code
// + REAL on-disk object count:
//   - no password / wrong password → exit 2 + TV-AUTH-01, and ZERO objects deleted
//     (the gate fires before SweepFederated);
//   - correct password → proceeds (exit 0) and the eligible blob IS swept.
//
// The gate runs the production path (gateLocation → auth.Gate → argon2id); only
// the SSH transport (verifier) and the all-members reachability ping are replaced
// by the harness (SetTestGateVerifier + SetTestGCProbe). A gc that forgot to call
// gateLocation would WRONGLY delete here — the exact 46.A regression.
func TestBlock4_GCRefusal(t *testing.T) {
	ctx := context.Background()

	objCount := func(t *testing.T, f *fedtest.Fed) int {
		keys, err := f.MemberBackend("home").List(ctx, "objects/")
		if err != nil {
			t.Fatalf("list objects: %v", err)
		}
		return len(keys)
	}

	// fixture builds a federation (members[0] = "home" holds an unreferenced
	// git-mode blob, gc-eligible: no committed repo lock references it) and a git
	// repo whose tailvault.toml storage points at "home" (chdir'd in), wiring the
	// gate + probe + tailscale seams. Pass extra member names to exercise the
	// all-members reachability gate. SetPassword/ClearPassword is left to the caller.
	fixture := func(t *testing.T, members ...string) *fedtest.Fed {
		if len(members) == 0 {
			members = []string{"home"}
		}
		f := fedtest.New(t, members...)
		SetTestGateVerifier(f.Verifier)
		t.Cleanup(func() { SetTestGateVerifier(nil) })
		SetTestGCProbe(f.Probe())
		t.Cleanup(func() { SetTestGCProbe(nil) })
		newTSClient = func() *tailscale.Client { return &tailscale.Client{R: okTSRunner{}} }
		t.Cleanup(func() { newTSClient = tailscale.New })

		// An unreferenced git-mode object on the node → a gc candidate.
		f.SeedGit(t, "home", "junk.bin", []byte("unreferenced blob"), nil)

		// A git repo whose storage points at "home" (gc is repo-side: findRepoRoot +
		// cfg.Storage.Location). No committed tailvault.lock → empty keep-set → the
		// seeded blob is eligible.
		repo := t.TempDir()
		gcGit(t, repo, "init")
		gcGit(t, repo, "config", "user.email", "demo@example.com")
		gcGit(t, repo, "config", "user.name", "demo")
		cfg := config.Default()
		cfg.Storage.Location = "home"
		if err := config.Write(filepath.Join(repo, "tailvault.toml"), &cfg); err != nil {
			t.Fatal(err)
		}
		gcGit(t, repo, "add", "-A")
		gcGit(t, repo, "commit", "-m", "init")

		cwd, _ := os.Getwd()
		t.Cleanup(func() { _ = os.Chdir(cwd) })
		if err := os.Chdir(repo); err != nil {
			t.Fatal(err)
		}
		return f
	}

	t.Run("wrong-password-refused", func(t *testing.T) {
		f := fixture(t)
		f.Member(t, "home").SetPassword(t, "correct-horse")
		before := objCount(t, f)
		if before == 0 {
			t.Fatal("precondition: an eligible object must exist")
		}

		_, code, err := execCLI("gc", "--password-file", pwFile(t, "wrong-guess"))
		if code != exitCfg || !isTVCode(err, tserr.AuthRequired) {
			t.Fatalf("wrong password: want exit 2 + TV-AUTH-01, got exit %d / %v", code, err)
		}
		if after := objCount(t, f); after != before {
			t.Errorf("wrong-password gc deleted %d object(s); the gate must refuse before any delete (zero)", before-after)
		}
	})

	t.Run("no-password-refused", func(t *testing.T) {
		f := fixture(t)
		f.Member(t, "home").ClearPassword(t) // protected node, `vault passwd` never run
		before := objCount(t, f)

		_, code, err := execCLI("gc", "--password-file", pwFile(t, "anything"))
		if code != exitCfg || !isTVCode(err, tserr.AuthRequired) {
			t.Fatalf("no password set: want exit 2 + TV-AUTH-01, got exit %d / %v", code, err)
		}
		if after := objCount(t, f); after != before {
			t.Errorf("no-password gc deleted %d object(s); want zero", before-after)
		}
	})

	t.Run("correct-password-proceeds-and-sweeps", func(t *testing.T) {
		f := fixture(t)
		f.Member(t, "home").SetPassword(t, "correct-horse")
		before := objCount(t, f)

		_, code, err := execCLI("gc", "--password-file", pwFile(t, "correct-horse"))
		if code != exitOK || err != nil {
			t.Fatalf("correct password must proceed (exit 0): got exit %d / %v", code, err)
		}
		if after := objCount(t, f); after >= before {
			t.Errorf("correct-password gc must sweep the eligible blob: object count %d → %d (expected a drop)", before, after)
		}
	})

	// gc-needs-all-members: the stubbed probe only flips REACHABILITY; the REAL
	// PlanFederated all-members gate decides. With a member down, federated gc must
	// refuse with TV-FED-02 (exit 6) BEFORE planning a deletion — even with the
	// correct password (the reachability gate precedes the password gate), and ZERO
	// blobs deleted. Proves the production gate runs behind SetTestGCProbe.
	t.Run("member-down-needs-all-TVFED02", func(t *testing.T) {
		f := fixture(t, "home", "office")
		f.Member(t, "home").SetPassword(t, "correct-horse")
		f.Member(t, "office").SetDown(true) // a federation member is unreachable
		before := objCount(t, f)

		_, code, err := execCLI("gc", "--password-file", pwFile(t, "correct-horse"))
		if code != exitFed || !isTVCode(err, tserr.FedNeedAllMembers) {
			t.Fatalf("a down member must fail federated gc with TV-FED-02 (exit 6), got exit %d / %v", code, err)
		}
		if after := objCount(t, f); after != before {
			t.Errorf("gc under a partial view deleted %d object(s); the all-members gate must refuse before any delete (zero)", before-after)
		}
	})
}

func gcGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
