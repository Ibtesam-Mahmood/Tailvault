package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/auth"
	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fedtest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// TestBlock4_AuthMatrix_RemainingGated extends the §16 behavioral matrix to fed
// evict: driven through the REAL root Execute() against a protected survivor with a
// wrong / absent password, it must refuse with exit 2 + TV-AUTH-01, leaving the
// survivor's catalog BYTE-UNCHANGED (the gate fires before any roster mutation).
// With Row 1 (mv/rm/sync-mode/fed-leave) + Row 2 (gc) this covers 6 of the 10 §16
// gated commands behaviorally; the merged gate-aware static audit independently
// proves ALL 10 actually call gateLocation. (rebuild-catalog guards its gate behind
// the remote/SSH path — ungated on a taildrive member, so the seam doesn't trigger
// it without an SSH stub — and restore-identity validates --receipt/--record/--lock
// before the gate; both rest on the static audit + their unit tests + Row 5's
// restore coverage. join/passwd-change likewise — see the review-50 coverage note.)
func TestBlock4_AuthMatrix_RemainingGated(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *fedtest.Fed)
		args  func(pwFile string) []string
	}{
		{
			// evict gates the SURVIVOR it writes through (home); the target (office)
			// must be DOWN or evict refuses it as reachable before the gate — so set
			// office down via the backend seam, leaving home the gated survivor.
			name:  "fed-evict",
			setup: func(t *testing.T, f *fedtest.Fed) { f.Member(t, "office").SetDown(true) },
			args: func(pw string) []string {
				return []string{"fed", "evict", "office", "--password-file", pw}
			},
		},
		{
			// rebuild-catalog gates UNCONDITIONALLY (the seam reaches it), but AFTER
			// the overwrite-confirm prompt — so --yes is required or a non-TTY drive
			// aborts "n" before the gate. With --yes + a protected member, a wrong pw
			// refuses before the catalog is overwritten.
			name:  "rebuild-catalog",
			setup: func(t *testing.T, f *fedtest.Fed) {},
			args: func(pw string) []string {
				return []string{"vault", "rebuild-catalog", "home", "--yes", "--password-file", pw}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		for _, st := range []struct {
			label string
			clear bool
		}{{"wrong-pw", false}, {"no-pw", true}} {
			st := st
			t.Run(tc.name+"/"+st.label, func(t *testing.T) {
				f := fedtest.New(t, "home", "office")
				SetTestGateVerifier(f.Verifier)
				t.Cleanup(func() { SetTestGateVerifier(nil) })
				SetTestBackendFor(func(name string) (backend.Backend, bool) {
					if b := f.MemberBackend(name); b != nil {
						return b, true
					}
					return nil, false
				})
				t.Cleanup(func() { SetTestBackendFor(nil) })
				tc.setup(t, f)
				if st.clear {
					f.Member(t, "home").ClearPassword(t) // protected node, no password set
				} else {
					f.Member(t, "home").SetPassword(t, "correct-horse")
				}

				catPath := filepath.Join(f.Member(t, "home").Root, "meta", "catalog.toml")
				before, _ := os.ReadFile(catPath)

				_, code, err := execCLI(tc.args(pwFile(t, "wrong-guess"))...)
				if code != exitCfg || !isTVCode(err, tserr.AuthRequired) {
					t.Fatalf("%s/%s: want exit 2 + TV-AUTH-01, got exit %d / %v", tc.name, st.label, code, err)
				}
				if after, _ := os.ReadFile(catPath); string(after) != string(before) {
					t.Errorf("%s/%s: a gated refusal must leave the catalog byte-unchanged", tc.name, st.label)
				}
			})
		}
	}
}

// TestBlock4_Auth_PasswdChangeRefused pins passwd-change's gate-before-mutate
// ordering (qa-review): with an existing hash on the node (so it's a CHANGE, gated
// on the OLD password) and a protected member, a wrong old password refuses with
// TV-AUTH-01 and leaves the hash file BYTE-UNCHANGED — proving the new password is
// never written before the gate rejects (a gate-after-write regression would flip
// the bytes).
func TestBlock4_Auth_PasswdChangeRefused(t *testing.T) {
	f := fedtest.New(t, "home")
	SetTestGateVerifier(f.Verifier)
	t.Cleanup(func() { SetTestGateVerifier(nil) })
	home := f.Member(t, "home").Root

	// Existing hash on disk → passwd treats the run as a CHANGE (gates on old pw).
	hf, err := auth.NewHashFile([]byte("old-secret"))
	if err != nil {
		t.Fatal(err)
	}
	hashPath := filepath.Join(home, filepath.FromSlash(auth.HashFileRel))
	if err := auth.WriteHashFile(hashPath, hf); err != nil {
		t.Fatal(err)
	}
	f.Member(t, "home").SetPassword(t, "old-secret") // the seam verifier for the gate
	before, _ := os.ReadFile(hashPath)

	_, code, err := execCLI("vault", "passwd", "home",
		"--password-file", pwFile(t, "wrong-old"), "--new-password-file", pwFile(t, "new-secret"))
	if code != exitCfg || !isTVCode(err, tserr.AuthRequired) {
		t.Fatalf("passwd change wrong old pw: want exit 2 + TV-AUTH-01, got exit %d / %v", code, err)
	}
	if after, _ := os.ReadFile(hashPath); string(after) != string(before) {
		t.Error("a rejected passwd change must leave the hash file byte-unchanged (gate before write)")
	}
}

// TestBlock4_Auth_FedJoinRefused pins fed join's gate-before-mutate ordering
// (qa-review): joining a non-member location into a federation whose sponsor member
// is protected, with a wrong password, refuses with TV-AUTH-01 and leaves the
// sponsor's roster UNCHANGED (the joiner is never added) — proving the Phase-1
// gate-all precedes any roster write.
func TestBlock4_Auth_FedJoinRefused(t *testing.T) {
	f := fedtest.New(t, "home") // home is the federated sponsor
	SetTestGateVerifier(f.Verifier)
	t.Cleanup(func() { SetTestGateVerifier(nil) })
	f.Member(t, "home").SetPassword(t, "sponsor-pw")

	// Register a fresh, NON-member location to join.
	reg, err := locations.Load()
	if err != nil {
		t.Fatal(err)
	}
	newbieDir := t.TempDir()
	reg.Locations["newbie"] = locations.Location{Node: "newbie", BasePath: newbieDir, Backend: locations.BackendTaildrive, Share: "v"}
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
	writeBareVault(t, newbieDir, nil) // initialised but un-federated → joinable

	homeRoster := func() int { return len(fedMembers(t, f.Member(t, "home").Root)) }
	before := homeRoster()

	_, code, err := execCLI("fed", "join", "newbie", "--password-file", pwFile(t, "wrong"))
	if code != exitCfg || !isTVCode(err, tserr.AuthRequired) {
		t.Fatalf("fed join wrong pw: want exit 2 + TV-AUTH-01, got exit %d / %v", code, err)
	}
	if after := homeRoster(); after != before {
		t.Errorf("a rejected fed join must leave the sponsor roster unchanged (gate before roster write): %d → %d", before, after)
	}
}

// TestBlock4_Auth_RestoreIdentityRefused pins restore-identity's gate-before-mutate
// ordering (qa-review): with a valid receipt (so flag-validation passes) for a
// NOT-live id (so the federation collision guard doesn't fire first) and a
// protected member, a wrong password refuses with TV-AUTH-01 and leaves the catalog
// BYTE-UNCHANGED — the gate precedes the identity rewrite.
func TestBlock4_Auth_RestoreIdentityRefused(t *testing.T) {
	f := fedtest.New(t, "home")
	SetTestGateVerifier(f.Verifier)
	t.Cleanup(func() { SetTestGateVerifier(nil) })
	home := f.Member(t, "home").Root
	f.Seed(t, "home", "media/a.txt", []byte("payload")) // the restore target
	f.Member(t, "home").SetPassword(t, "node-pw")

	// A receipt for a FRESH genesis (id live nowhere) → collision guard passes and
	// execution reaches the gate.
	g := identity.Genesis{
		ContentSHA256: strings.Repeat("a", 64), OriginalPath: "media/orig.txt",
		IngestOpID: "op-restore", OriginNode: "home",
	}
	id, err := identity.MintID(g)
	if err != nil {
		t.Fatal(err)
	}
	recDir := t.TempDir()
	if err := identity.WriteReceipt(recDir, identity.Receipt{
		ID: id, Genesis: g, Path: "home/media/a.txt", SHA256AtPull: g.ContentSHA256,
		PulledAt: time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC), SourceNode: "home",
	}); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(recDir, id+".toml")

	catPath := filepath.Join(home, "meta", "catalog.toml")
	before, _ := os.ReadFile(catPath)

	_, code, err := execCLI("vault", "restore-identity", "home/media/a.txt",
		"--receipt", receiptPath, "--yes", "--password-file", pwFile(t, "wrong"))
	if code != exitCfg || !isTVCode(err, tserr.AuthRequired) {
		t.Fatalf("restore-identity wrong pw: want exit 2 + TV-AUTH-01, got exit %d / %v", code, err)
	}
	if after, _ := os.ReadFile(catPath); string(after) != string(before) {
		t.Error("a rejected restore-identity must leave the catalog byte-unchanged (gate before rewrite)")
	}
}
