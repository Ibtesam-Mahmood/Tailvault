package auth

import (
	"context"
	"errors"
)

// ErrWrongPassword indicates a candidate password did not match the node's
// stored hash. Distinct from ErrNoPassword ("none set") so the command boundary
// can render the right message — both map to TV-AUTH-01 (exit bucket 2).
var ErrWrongPassword = errors.New("auth: password rejected")

// Gate is the client-side authorization step for a mutating remote op: it
// obtains a candidate password (opts: --password-file / TAILVAULT_PASSWORD /
// no-echo TTY) and checks it with v, which for the real path runs the node-side
// verifier over SSH so the stored hash never leaves the node.
//
// Returns nil only on a confirmed match. ErrNoPassword (node has none set),
// ErrWrongPassword (rejected), ErrNoPasswordSource (no way to obtain one), or an
// operational error from v propagate unchanged; the command boundary wraps them
// as tserr.AuthErr (TV-AUTH-01) so the op is refused before any work. The
// candidate bytes are scrubbed before return.
func Gate(ctx context.Context, v Verifier, opts ReadOpts) error {
	pw, err := ReadPassword(opts)
	if err != nil {
		return err
	}
	defer Zero(pw)
	ok, err := v.VerifyPassword(ctx, pw)
	if err != nil {
		return err
	}
	if !ok {
		return ErrWrongPassword
	}
	return nil
}
