package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Ibtesam-Mahmood/tailvault/internal/auth"
	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// sshVerifier authorizes a mutating remote op by running `tailvault node
// verify-passwd` ON the node over the SSH channel. The candidate password is
// piped to the node's stdin VERBATIM (no trailing newline — matching the
// node-side verbatim read); the node loads its LOCAL hash file, compares, and
// only the exit status crosses back. The stored hash NEVER leaves the node —
// the §16 / D9 property — and verification is never done client-side. It
// satisfies auth.Verifier.
type sshVerifier struct {
	ssh       *backend.SSH
	vaultBase string // node base_path of the vault (the node joins meta/auth/passwd)
}

// VerifyPassword reports whether candidate matches the node's stored password.
// A clean reject is (false, nil); a node with no password set is (false,
// auth.ErrNoPassword); an unreachable node / transport failure propagates as a
// non-nil error (typically TV-NODE-01 from Exec's preflight) — distinct from a
// verdict.
func (v sshVerifier) VerifyPassword(ctx context.Context, candidate []byte) (bool, error) {
	cmd := fmt.Sprintf("tailvault node verify-passwd --vault %s", backend.ShellQuote(v.vaultBase))
	// Verbatim stdin: exactly the candidate bytes, no added newline.
	stderr, err := v.ssh.Exec(ctx, bytes.NewReader(candidate), cmd)
	if err == nil {
		return true, nil // node exited 0 → match
	}
	// The node prints its typed "TV-…: cause (fix: …)" line to stderr (main.go).
	// A TV-AUTH-01 is the node's clean verdict; anything else is an ssh/transport
	// failure to surface, never silently treated as a wrong password.
	s := string(stderr)
	switch {
	case strings.Contains(s, "no vault password set"):
		return false, auth.ErrNoPassword
	case strings.Contains(s, string(tserr.AuthRequired)): // "TV-AUTH-01" → rejected / unreadable hash
		return false, nil
	default:
		return false, err // unreachable node / transport / unexpected — not a verdict
	}
}

// testGateVerifier is a TEST SEAM (nil in production). When installed via
// SetTestGateVerifier (the fedtest harness only), gateLocation consults it to
// obtain the Verifier for a location by name: returning (verifier, true) makes
// that member password-gated against an in-memory hash (no SSH); returning
// (nil, false) leaves the normal SSH-only logic in force. This lets the §16
// behavioral auth matrix (fix-46 46.C / task-50) drive the REAL gate path —
// auth.Gate → Verifier.VerifyPassword → argon2id — with no SSH and without
// replacing gateLocation itself (only the transport/verifier is in-memory).
var testGateVerifier func(name string) (auth.Verifier, bool)

// SetTestGateVerifier installs (or clears, with nil) the test gate-verifier seam.
// TEST-ONLY: production code never calls it; when nil, gateLocation's SSH path is
// exactly as before (taildrive/local ungated, DEV-46.8).
func SetTestGateVerifier(fn func(name string) (auth.Verifier, bool)) { testGateVerifier = fn }

// gateLocation enforces the per-node password for a MUTATING op on a location
// (D9 / SPEC v2 §16) — call it BEFORE any WAL intent / byte move. Verification is
// done ON THE NODE over SSH (the stored hash never leaves the node). Every
// refusal — none set, rejected, no source — maps to TV-AUTH-01 (exit 2), so the
// op is refused before any work. READS MUST NOT call this (§16). Shared by
// mv/rm/sync-mode/passwd/evict so the gate + its error mapping live in exactly
// one place (Block 5 audits one surface).
//
// SSH-only by design (DEV-46.8): a passive Taildrive mount cannot run the
// node-side verifier, and reading the hash to the client would violate §16's
// "hash never leaves the node". So a non-SSH (taildrive/local) location is NOT
// password-gated here — its mutations rely on the tailnet ACL + the OS mount
// permissions (documented; the threat model revisits this in Block 5). Returning
// nil for non-SSH is deliberate, not a silent skip of a required check.
//
// TEST SEAM: when testGateVerifier is installed (fedtest harness only), it may
// supply the Verifier for a protected member by name, so the behavioral §16 auth
// matrix (fix-46 46.C / task-50) drives the REAL gate path — auth.Gate →
// Verifier.VerifyPassword → argon2id — against an in-memory password with NO SSH,
// WITHOUT replacing gateLocation. It is nil in production; the SSH path is
// unchanged.
func gateLocation(ctx context.Context, loc locations.Location, b backend.Backend, name, passwordFile string) error {
	var v auth.Verifier
	if testGateVerifier != nil {
		if tv, ok := testGateVerifier(name); ok {
			v = tv // harness-managed protected member (in-memory verifier)
		}
	}
	if v == nil {
		s, ok := b.(*backend.SSH)
		if !ok {
			return nil // taildrive/local: ungated (no node-side verify possible)
		}
		v = sshVerifier{ssh: s, vaultBase: loc.BasePath}
	}
	err := auth.Gate(ctx, v, auth.ReadOpts{
		PasswordFile: passwordFile,
		Prompt:       "Password for " + name + ": ",
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrNoPassword):
		return tserr.AuthErr("no vault password set on "+name+" — run `tailvault vault passwd "+name+"`", err)
	case errors.Is(err, auth.ErrWrongPassword):
		return tserr.AuthErr("password rejected for "+name, err)
	case errors.Is(err, auth.ErrNoPasswordSource):
		return tserr.AuthErr("password required for "+name+" (set TAILVAULT_PASSWORD, pass --password-file, or run on a terminal)", err)
	default:
		return err // operational failure (node unreachable, etc.) — already typed or plain
	}
}
