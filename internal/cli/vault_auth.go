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

// localVerifier verifies a candidate against a hash file read over a local or
// mounted path (taildrive) — the only option for a passive share with no remote
// exec. ACCEPTED LIMITATION: the hash is read over the mount (it leaves the node
// via the network filesystem), unlike the SSH path where it never leaves the
// node. SSH is the hardened backend; taildrive auth is best-effort (Block 5).
type localVerifier struct{ base string }

// VerifyPassword implements auth.Verifier against the local/mounted hash file.
func (v localVerifier) VerifyPassword(_ context.Context, candidate []byte) (bool, error) {
	hf, ok, err := auth.LoadHashFile(auth.HashFilePath(v.base))
	if err != nil {
		return false, err
	}
	if !ok {
		return false, auth.ErrNoPassword
	}
	return auth.Verify(hf, candidate), nil
}

// verifierFor selects the auth.Verifier for a location: SSH runs the node-side
// verifier (hash never leaves the node); taildrive/local reads the hash over the
// mount (accepted limitation).
func verifierFor(loc locations.Location, b backend.Backend) auth.Verifier {
	if s, ok := b.(*backend.SSH); ok {
		return sshVerifier{ssh: s, vaultBase: loc.BasePath}
	}
	return localVerifier{base: loc.BasePath}
}

// gateLocation enforces the per-node password for a MUTATING op on a location
// (D9 / SPEC v2 §16) — call it BEFORE any WAL intent / byte move. It obtains a
// candidate (--password-file / TAILVAULT_PASSWORD / no-echo TTY) and verifies it
// (node-side for SSH). Every refusal — none set, rejected, no source — maps to
// TV-AUTH-01 (exit 2), so the op is refused before any work. READS MUST NOT call
// this (§16). Shared by track/put/mv/rm/sync-mode/passwd so the gate + its
// error mapping live in exactly one place (Block 5 audits one surface).
func gateLocation(ctx context.Context, loc locations.Location, b backend.Backend, name, passwordFile string) error {
	err := auth.Gate(ctx, verifierFor(loc, b), auth.ReadOpts{
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
