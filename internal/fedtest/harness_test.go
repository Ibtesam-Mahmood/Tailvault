package fedtest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/auth"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// TestThreeMemberFederationConcise demonstrates the acceptance criterion: a
// 3-member federation with a seeded file is expressible in a handful of lines,
// and the seeded file resolves FoundAtHome.
func TestThreeMemberFederationConcise(t *testing.T) {
	f := New(t, "home", "office", "cloud") // 3 members, shared roster
	file := f.Seed(t, "home", "a.bin", []byte("hello"))

	res, err := f.Resolver().Resolve(context.Background(), file.ID, "home")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != fed.FoundAtHome {
		t.Fatalf("want FoundAtHome, got %s", res.Outcome)
	}
}

// TestDownMemberPartialView: with the home member down, a fan-out cannot prove
// absence → PartialView (never a false Missing).
func TestDownMemberPartialView(t *testing.T) {
	f := New(t, "home", "office")
	file := f.Seed(t, "home", "a.bin", []byte("bytes"))

	f.Member(t, "home").SetDown(true)
	res, err := f.Resolver().Resolve(context.Background(), file.ID, "home")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != fed.PartialView {
		t.Fatalf("a down home must yield PartialView, got %s", res.Outcome)
	}
}

// TestMissingWhenAllUp: an id no member holds, with everyone reachable, is a
// provable Missing.
func TestMissingWhenAllUp(t *testing.T) {
	f := New(t, "home", "office")
	f.Seed(t, "home", "a.bin", []byte("bytes"))

	res, err := f.Resolver().Resolve(context.Background(), "deadbeef"+"00000000000000000000000000000000000000000000000000000000", "home")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != fed.Missing {
		t.Fatalf("absent id with all members up must be Missing, got %s", res.Outcome)
	}
}

// TestTamperBreaksChain: corrupting the first of two WAL entries breaks the hash
// chain → wal.Read returns ErrChainBroken.
func TestTamperBreaksChain(t *testing.T) {
	f := New(t, "home")
	f.Seed(t, "home", "a.bin", []byte("one"))
	f.Seed(t, "home", "b.bin", []byte("two"))

	f.Tamper(t, "home", 0) // flip the genesis entry; seq-1's prev_hash no longer matches

	_, err := (&wal.Log{B: f.Member(t, "home").Backend()}).Read(context.Background())
	if !errors.Is(err, wal.ErrChainBroken) {
		t.Fatalf("tamper must break the chain (ErrChainBroken), got %v", err)
	}
}

// TestSeedGitAddsLockEntry: SeedGit puts the blob in objects/, ingests a git-mode
// catalog row, and appends a self-certifying v2 lock entry.
func TestSeedGitAddsLockEntry(t *testing.T) {
	f := New(t, "home")
	lk := &lock.Lock{Version: lock.SchemaVersion}
	file := f.SeedGit(t, "home", "doc.bin", []byte("git-bytes"), lk)

	if file.SyncMode != "git" {
		t.Errorf("SeedGit must produce a git-mode row, got %q", file.SyncMode)
	}
	if len(lk.Entries) != 1 || lk.Entries[0].ID != file.ID {
		t.Fatalf("SeedGit must append a matching lock entry: %+v", lk.Entries)
	}
	if err := lk.Validate(); err != nil {
		t.Errorf("the seeded lock must self-certify: %v", err)
	}
	// The blob is in the member's object store.
	if m, err := f.Member(t, "home").Backend().Stat(context.Background(), "objects/"+file.SHA256); err != nil || !m.Exists {
		t.Errorf("SeedGit must store objects/<sha>: exists=%v err=%v", m.Exists, err)
	}
}

// TestNewDemoRepo: the git-flow bridge fixture is wired (config, attributes,
// generated files straddling min_size, registered location).
func TestNewDemoRepo(t *testing.T) {
	f := New(t, "home")
	d := NewDemoRepo(t, f, "home", DemoOpt{})

	for _, p := range []string{"tailvault.toml", ".gitattributes"} {
		if _, err := os.Stat(filepath.Join(d.Dir, p)); err != nil {
			t.Errorf("demo repo missing %s: %v", p, err)
		}
	}
	if len(d.Big) == 0 || len(d.Small) == 0 {
		t.Fatalf("demo repo must generate files straddling min_size: big=%v small=%v", d.Big, d.Small)
	}
	for _, p := range d.Files {
		if _, err := os.Stat(filepath.Join(d.Dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("generated file %s missing: %v", p, err)
		}
	}
}

// TestAuthSeam validates the gate-verifier seam (the harness half coder-c's auth
// matrix + task-50 wire to cli.SetTestGateVerifier): SetPassword/ClearPassword
// toggle in-memory state, and Verifier returns the right auth.Verifier (real
// auth.Verify path) — unprotected→ungated, set→verifies, cleared→ErrNoPassword.
func TestAuthSeam(t *testing.T) {
	ctx := context.Background()
	f := New(t, "home", "office")

	// Unprotected (default) → ungated: no verifier installed.
	if _, ok := f.Verifier("home"); ok {
		t.Fatal("an unprotected member must be ungated (no verifier)")
	}

	// SetPassword → verifier accepts the right pw, rejects a wrong one.
	f.Member(t, "home").SetPassword(t, "hunter2")
	v, ok := f.Verifier("home")
	if !ok {
		t.Fatal("a protected member must return a verifier")
	}
	if good, err := v.VerifyPassword(ctx, []byte("hunter2")); err != nil || !good {
		t.Errorf("correct password must verify: ok=%v err=%v", good, err)
	}
	if bad, err := v.VerifyPassword(ctx, []byte("wrong")); err != nil || bad {
		t.Errorf("wrong password must be rejected (false,nil): ok=%v err=%v", bad, err)
	}

	// ClearPassword → protected but no password → ErrNoPassword.
	f.Member(t, "home").ClearPassword(t)
	v2, ok := f.Verifier("home")
	if !ok {
		t.Fatal("a cleared (still protected) member must return a verifier")
	}
	if _, err := v2.VerifyPassword(ctx, []byte("anything")); err != auth.ErrNoPassword {
		t.Errorf("a cleared member must surface ErrNoPassword, got %v", err)
	}

	// An unknown member → ungated.
	if _, ok := f.Verifier("ghost"); ok {
		t.Error("an unknown member must be ungated")
	}
}
