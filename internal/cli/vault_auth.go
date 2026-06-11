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
func gateLocation(ctx context.Context, loc locations.Location, b backend.Backend, name, passwordFile string) error {
	s, ok := b.(*backend.SSH)
	if !ok {
		return nil // taildrive/local: ungated (no node-side verify possible)
	}
	err := auth.Gate(ctx, sshVerifier{ssh: s, vaultBase: loc.BasePath}, auth.ReadOpts{
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
