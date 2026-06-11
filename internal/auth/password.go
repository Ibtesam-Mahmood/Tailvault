package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"
)

// EnvPassword is the environment variable a candidate password may be read from
// for non-interactive use (scripts, tests). A bare `--password` argv flag is
// deliberately NOT supported — it would be visible in `ps`.
const EnvPassword = "TAILVAULT_PASSWORD"

// ErrNoPassword indicates the node has no password configured. Mutations are
// refused (never defaulted-open); the command boundary turns this into a
// TV-AUTH-01 telling the user to run `tailvault vault passwd <location>`.
var ErrNoPassword = errors.New("auth: no vault password set on node")

// ErrNoPasswordSource indicates no candidate password could be obtained: not on
// a TTY and neither TAILVAULT_PASSWORD nor --password-file was provided. The op
// must hard-fail here, before any network mutation.
var ErrNoPasswordSource = errors.New("auth: no password source (set " + EnvPassword + ", pass --password-file, or run on a terminal)")

// ReadOpts selects where a candidate password comes from and how it is prompted.
type ReadOpts struct {
	// PasswordFile, when non-empty, is read for the password (a --password-file
	// path). Takes precedence over the environment and the TTY.
	PasswordFile string
	// Prompt is the TTY prompt text (interactive path only).
	Prompt string
	// Confirm, on the interactive path, prompts a second time and requires the
	// two entries to match — used when SETTING a new password.
	Confirm bool
	// Stdin/Stderr indirect the terminal for tests; nil means os.Stdin/os.Stderr.
	Stdin  *os.File
	Stderr *os.File
}

// ReadPassword obtains a candidate password from, in precedence order:
//  1. opts.PasswordFile (a single trailing newline is stripped; other bytes,
//     including spaces, are preserved),
//  2. the TAILVAULT_PASSWORD environment variable,
//  3. a no-echo TTY prompt (twice, matched, when opts.Confirm).
//
// On a non-terminal with neither file nor env set it returns ErrNoPasswordSource
// rather than blocking — the caller must fail before touching the network.
func ReadPassword(opts ReadOpts) ([]byte, error) {
	if opts.PasswordFile != "" {
		data, err := os.ReadFile(opts.PasswordFile)
		if err != nil {
			return nil, fmt.Errorf("auth: read --password-file: %w", err)
		}
		return stripOneTrailingNewline(data), nil
	}
	if v, ok := os.LookupEnv(EnvPassword); ok {
		return []byte(v), nil
	}

	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	fd := int(stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, ErrNoPasswordSource
	}

	prompt := opts.Prompt
	if prompt == "" {
		prompt = "Password: "
	}
	fmt.Fprint(stderr, prompt)
	pw, err := term.ReadPassword(fd)
	fmt.Fprintln(stderr)
	if err != nil {
		return nil, fmt.Errorf("auth: read password: %w", err)
	}
	if opts.Confirm {
		fmt.Fprint(stderr, "Confirm password: ")
		again, err := term.ReadPassword(fd)
		fmt.Fprintln(stderr)
		if err != nil {
			Zero(pw)
			return nil, fmt.Errorf("auth: read password confirmation: %w", err)
		}
		if !bytes.Equal(pw, again) {
			Zero(pw)
			Zero(again)
			return nil, errors.New("auth: passwords do not match")
		}
		Zero(again)
	}
	return pw, nil
}

// stripOneTrailingNewline removes a single trailing "\n" or "\r\n", returning a
// copy so the caller can scrub it independently of the file buffer.
func stripOneTrailingNewline(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	b = bytes.TrimSuffix(b, []byte("\r"))
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// Zero overwrites b with zeros — a best-effort scrub of password bytes after
// use. Never relied on for security guarantees (the GC may have copied them),
// but it shrinks the window a secret sits in memory.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Verifier checks a candidate password against a node's stored hash. The
// production verifier (added with the remote commands) shells `tailvault node
// verify-passwd` over SSH so the hash never leaves the node; MemoryVerifier
// backs the multi-node harness and unit tests with no SSH.
//
// A non-nil error means the verification could not be performed (node
// unreachable, no password configured); a (false, nil) means a wrong password.
type Verifier interface {
	VerifyPassword(ctx context.Context, candidate []byte) (bool, error)
}

// MemoryVerifier verifies against an in-memory hash file. Set=false models a
// node with no password configured, returning ErrNoPassword.
type MemoryVerifier struct {
	HF  HashFile
	Set bool
}

// VerifyPassword implements Verifier against the in-memory hash.
func (m MemoryVerifier) VerifyPassword(_ context.Context, candidate []byte) (bool, error) {
	if !m.Set {
		return false, ErrNoPassword
	}
	return Verify(m.HF, candidate), nil
}
