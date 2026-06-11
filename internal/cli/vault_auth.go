package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/Ibtesam-Mahmood/tailvault/internal/auth"
	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
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
