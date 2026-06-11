package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/auth"
	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
)

// fakeRunner simulates `ssh user@node <remoteCmd>` for the SSH backend: it
// records the remote command + drained stdin and returns canned stderr/err so
// the node-side verify-passwd exit is simulated without a real node.
type fakeRunner struct {
	handle  func(remoteCmd string, stdin []byte) (stderr []byte, err error)
	lastCmd string
	lastIn  []byte
}

func (r *fakeRunner) Run(_ context.Context, in io.Reader, _ io.Writer, _ string, args ...string) ([]byte, error) {
	r.lastCmd = args[len(args)-1]
	if in != nil {
		r.lastIn, _ = io.ReadAll(in)
	}
	if r.handle == nil {
		return nil, nil
	}
	return r.handle(r.lastCmd, r.lastIn)
}

func newVerifier(r backend.Runner) sshVerifier {
	return sshVerifier{
		ssh:       &backend.SSH{User: "ibte", Node: "home-pi", BasePath: "/mnt/ssd/tv", R: r},
		vaultBase: "/mnt/ssd/tv",
	}
}

func TestSSHVerifier_Match(t *testing.T) {
	r := &fakeRunner{handle: func(string, []byte) ([]byte, error) { return nil, nil }} // exit 0
	ok, err := newVerifier(r).VerifyPassword(context.Background(), []byte("pw"))
	if err != nil || !ok {
		t.Fatalf("match: ok %v err %v; want true, nil", ok, err)
	}
	if !strings.Contains(r.lastCmd, "node verify-passwd --vault '/mnt/ssd/tv'") {
		t.Errorf("remote cmd = %q; want node verify-passwd with quoted --vault", r.lastCmd)
	}
}

func TestSSHVerifier_PipesCandidateVerbatim(t *testing.T) {
	// The candidate must reach the node's stdin with NO added newline.
	r := &fakeRunner{handle: func(string, []byte) ([]byte, error) { return nil, nil }}
	_, _ = newVerifier(r).VerifyPassword(context.Background(), []byte("p@ss word"))
	if string(r.lastIn) != "p@ss word" {
		t.Errorf("piped stdin = %q, want exactly %q (verbatim, no newline)", r.lastIn, "p@ss word")
	}
}

func TestSSHVerifier_Rejected(t *testing.T) {
	r := &fakeRunner{handle: func(string, []byte) ([]byte, error) {
		return []byte("TV-AUTH-01: password rejected (fix: ...)\n"), errors.New("exit status 2")
	}}
	ok, err := newVerifier(r).VerifyPassword(context.Background(), []byte("wrong"))
	if ok || err != nil {
		t.Errorf("rejected: ok %v err %v; want false, nil", ok, err)
	}
}

func TestSSHVerifier_NoPasswordSet(t *testing.T) {
	r := &fakeRunner{handle: func(string, []byte) ([]byte, error) {
		return []byte("TV-AUTH-01: no vault password set on node (fix: ...)\n"), errors.New("exit status 2")
	}}
	ok, err := newVerifier(r).VerifyPassword(context.Background(), []byte("x"))
	if ok || !errors.Is(err, auth.ErrNoPassword) {
		t.Errorf("no-password-set: ok %v err %v; want false, ErrNoPassword", ok, err)
	}
}

func TestSSHVerifier_NodeUnreachable_NotAVerdict(t *testing.T) {
	// An ssh-level failure (no TV-AUTH-01 marker) must NOT be read as a wrong
	// password — it propagates as an error.
	r := &fakeRunner{handle: func(string, []byte) ([]byte, error) {
		return []byte("ssh: connect to host home-pi port 22: No route to host\n"), errors.New("exit status 255")
	}}
	ok, err := newVerifier(r).VerifyPassword(context.Background(), []byte("pw"))
	if ok || err == nil {
		t.Errorf("unreachable: ok %v err %v; want false, non-nil error (not a verdict)", ok, err)
	}
}

func TestSSHVerifier_PingFailure_TVNODE01(t *testing.T) {
	r := &fakeRunner{}
	v := newVerifier(r)
	v.ssh.Ping = func(context.Context, string) error { return errors.New("100% loss") }
	ok, err := v.VerifyPassword(context.Background(), []byte("pw"))
	if ok || err == nil {
		t.Errorf("ping fail: ok %v err %v; want false + TV-NODE-01 error", ok, err)
	}
	if r.lastCmd != "" {
		t.Errorf("verify-passwd ran despite ping failure (cmd %q); preflight must abort first", r.lastCmd)
	}
}

// Compile-time assertion that sshVerifier satisfies auth.Verifier.
var _ auth.Verifier = sshVerifier{}
