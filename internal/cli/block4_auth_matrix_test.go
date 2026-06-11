package cli

import (
	"context"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fedtest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// TestBlock4_AuthMatrix is the §16 BEHAVIORAL auth proof (fix-46 46.C / SG-4): it
// drives each gated mutating command through the REAL root Cobra command against a
// password-PROTECTED fedtest member and asserts that — with no or a wrong password
// — the command refuses with TV-AUTH-01 (exit bucket 2) and appends ZERO WAL
// entries (the gate fires BEFORE any intent), while the correct password proceeds.
// The gate runs the production path (gateLocation → auth.Gate → argon2id); only
// the SSH transport is replaced by the harness in-memory verifier (cli.
// SetTestGateVerifier(f.Verifier)). A command that forgot to call gateLocation
// would WRONGLY succeed here — which is exactly what a marker-only audit can't see.
func TestBlock4_AuthMatrix(t *testing.T) {
	ctx := context.Background()

	// Each gated command: how to seed what it needs, and its argv (the --password-file
	// is filled per password-state).
	cases := []struct {
		name string
		seed func(t *testing.T, f *fedtest.Fed)
		args func(pwFile string) []string
	}{
		{
			name: "mv",
			seed: func(t *testing.T, f *fedtest.Fed) { f.Seed(t, "home", "media/a.txt", []byte("payload")) },
			args: func(pw string) []string {
				return []string{"vault", "mv", "home/media/a.txt", "home/media/b.txt", "--password-file", pw}
			},
		},
		{
			name: "rm",
			seed: func(t *testing.T, f *fedtest.Fed) { f.Seed(t, "home", "media/a.txt", []byte("payload")) },
			args: func(pw string) []string {
				return []string{"vault", "rm", "home/media/a.txt", "--yes", "--password-file", pw}
			},
		},
		{
			// Seed a GIT file and flip git→manual: no node re-hash (only →git
			// re-hashes), so the correct-password path proceeds cleanly while still
			// exercising the gate.
			name: "sync-mode",
			seed: func(t *testing.T, f *fedtest.Fed) { f.SeedGit(t, "home", "media/a.txt", []byte("payload"), nil) },
			args: func(pw string) []string {
				return []string{"vault", "sync-mode", "home/media/a.txt", "manual", "--password-file", pw}
			},
		},
		{
			// fed leave is a gated roster op (a different command family than the vault
			// mutators): the member is already federated by fedtest.New, so no seed.
			// The gate fires on the member's own roster write before any roster intent.
			name: "fed-leave",
			seed: func(t *testing.T, f *fedtest.Fed) {},
			args: func(pw string) []string {
				return []string{"fed", "leave", "home", "--password-file", pw}
			},
		},
	}

	walCount := func(t *testing.T, f *fedtest.Fed) int {
		recs, err := (&wal.Log{B: backend.NewTaildrive(f.Member(t, "home").Root)}).Read(ctx)
		if err != nil {
			t.Fatalf("read wal: %v", err)
		}
		return len(recs)
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name+"/wrong-password-refused", func(t *testing.T) {
			f := fedtest.New(t, "home")
			SetTestGateVerifier(f.Verifier)
			t.Cleanup(func() { SetTestGateVerifier(nil) })
			tc.seed(t, f)
			f.Member(t, "home").SetPassword(t, "correct-horse")
			before := walCount(t, f)

			_, code, err := execCLI(tc.args(pwFile(t, "wrong-guess"))...)
			if code != exitCfg || !isTVCode(err, tserr.AuthRequired) {
				t.Fatalf("%s wrong password: want exit 2 + TV-AUTH-01, got exit %d / %v", tc.name, code, err)
			}
			if after := walCount(t, f); after != before {
				t.Errorf("%s wrong password appended %d WAL entries; gate must refuse before any intent (zero)", tc.name, after-before)
			}
		})

		t.Run(tc.name+"/no-password-refused", func(t *testing.T) {
			f := fedtest.New(t, "home")
			SetTestGateVerifier(f.Verifier)
			t.Cleanup(func() { SetTestGateVerifier(nil) })
			tc.seed(t, f)
			f.Member(t, "home").ClearPassword(t) // protected node, `vault passwd` never run
			before := walCount(t, f)

			_, code, err := execCLI(tc.args(pwFile(t, "anything"))...)
			if code != exitCfg || !isTVCode(err, tserr.AuthRequired) {
				t.Fatalf("%s no password set: want exit 2 + TV-AUTH-01, got exit %d / %v", tc.name, code, err)
			}
			if after := walCount(t, f); after != before {
				t.Errorf("%s no-password appended %d WAL entries; want zero", tc.name, after-before)
			}
		})

		t.Run(tc.name+"/correct-password-proceeds", func(t *testing.T) {
			f := fedtest.New(t, "home")
			SetTestGateVerifier(f.Verifier)
			t.Cleanup(func() { SetTestGateVerifier(nil) })
			tc.seed(t, f)
			f.Member(t, "home").SetPassword(t, "correct-horse")
			before := walCount(t, f)

			if _, code, err := execCLI(tc.args(pwFile(t, "correct-horse"))...); code != exitOK || err != nil {
				t.Fatalf("%s correct password must proceed (exit 0): got exit %d / %v", tc.name, code, err)
			}
			if after := walCount(t, f); after <= before {
				t.Errorf("%s correct password should have advanced the WAL (%d → %d)", tc.name, before, after)
			}
		})
	}
}

// TestBlock4_AuthMatrix_ReadsUngated confirms the dual: read commands run with NO
// password configured anywhere, even on a password-protected member (§16: reads
// ride the tailnet ACL + SSH alone, never gated).
func TestBlock4_AuthMatrix_ReadsUngated(t *testing.T) {
	f := fedtest.New(t, "home")
	SetTestGateVerifier(f.Verifier)
	t.Cleanup(func() { SetTestGateVerifier(nil) })
	file := f.Seed(t, "home", "media/a.txt", []byte("payload"))
	f.Member(t, "home").SetPassword(t, "correct-horse") // protected — reads must still work

	for _, args := range [][]string{
		{"vault", "ls", "home"},
		{"vault", "stat", "home/media/a.txt"},
		{"fed", "status"},
	} {
		if _, code, err := execCLI(args...); code != exitOK || err != nil {
			t.Errorf("read %v must run (exit 0) without a password on a protected member: exit %d / %v", args, code, err)
		}
	}
	_ = file
}
