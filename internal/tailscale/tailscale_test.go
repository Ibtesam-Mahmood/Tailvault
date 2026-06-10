package tailscale

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// fakeRunner returns canned output + error for any invocation, recording the
// args it was called with.
type fakeRunner struct {
	out      []byte
	err      error
	gotArgs  []string
	callable func(args []string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.gotArgs = args
	if f.callable != nil {
		return f.callable(args)
	}
	return f.out, f.err
}

func loadFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "status.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func TestStatus_ParsesFixture(t *testing.T) {
	c := &Client{R: &fakeRunner{out: loadFixture(t)}}
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: unexpected error: %v", err)
	}
	if !st.LoggedIn {
		t.Errorf("LoggedIn = false, want true")
	}
	if st.Self.DNSName != "laptop.tailnet-name.ts.net" || !st.Self.Online {
		t.Errorf("Self = %+v, want laptop.tailnet-name.ts.net online", st.Self)
	}
	if len(st.Peers) != 2 {
		t.Fatalf("got %d peers, want 2", len(st.Peers))
	}
	// Sorted by DNSName: home-pi before office-nas.
	want := []Peer{
		{DNSName: "home-pi.tailnet-name.ts.net", Online: true},
		{DNSName: "office-nas.tailnet-name.ts.net", Online: false},
	}
	for i, w := range want {
		if st.Peers[i] != w {
			t.Errorf("Peers[%d] = %+v, want %+v", i, st.Peers[i], w)
		}
	}
}

func TestStatus_AbsentBinary(t *testing.T) {
	c := &Client{R: &fakeRunner{err: &exec.Error{Name: "tailscale", Err: exec.ErrNotFound}}}
	_, err := c.Status(context.Background())
	assertCode(t, err, tserr.NetNotRunning)
}

func TestStatus_ErrNotFoundDirect(t *testing.T) {
	c := &Client{R: &fakeRunner{err: exec.ErrNotFound}}
	_, err := c.Status(context.Background())
	assertCode(t, err, tserr.NetNotRunning)
}

func TestStatus_DaemonDown_UnparseableOutput(t *testing.T) {
	// Daemon down: non-zero exit, no parseable JSON -> TV-NET-01.
	c := &Client{R: &fakeRunner{out: []byte("failed to connect to local tailscaled"), err: errors.New("exit status 1")}}
	_, err := c.Status(context.Background())
	assertCode(t, err, tserr.NetNotRunning)
}

func TestStatus_LoggedOut(t *testing.T) {
	c := &Client{R: &fakeRunner{out: []byte(`{"BackendState":"NeedsLogin"}`)}}
	st, err := c.Status(context.Background())
	assertCode(t, err, tserr.NetNotLoggedIn)
	if st.LoggedIn {
		t.Errorf("LoggedIn = true, want false on logged-out session")
	}
}

func TestStatus_MagicDNSTrimmed(t *testing.T) {
	c := &Client{R: &fakeRunner{out: loadFixture(t)}}
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, name := range append([]string{st.Self.DNSName}, dnsNames(st.Peers)...) {
		if name == "" {
			continue
		}
		if name[len(name)-1] == '.' {
			t.Errorf("DNSName %q still has trailing dot", name)
		}
	}
}

func TestPing_PassesArgsAndPropagatesError(t *testing.T) {
	fr := &fakeRunner{err: errors.New("ping: timeout")}
	c := &Client{R: fr}
	err := c.Ping(context.Background(), "home-pi.tailnet-name.ts.net")
	if err == nil {
		t.Fatalf("Ping: want error, got nil")
	}
	// Ping must NOT translate to a tserr code — that's the backend's job.
	var te *tserr.Error
	if errors.As(err, &te) {
		t.Errorf("Ping should return a plain error, got tserr %v", te)
	}
	wantArgs := []string{"ping", "--c", "1", "home-pi.tailnet-name.ts.net"}
	if len(fr.gotArgs) != len(wantArgs) {
		t.Fatalf("Ping args = %v, want %v", fr.gotArgs, wantArgs)
	}
	for i := range wantArgs {
		if fr.gotArgs[i] != wantArgs[i] {
			t.Errorf("Ping args[%d] = %q, want %q", i, fr.gotArgs[i], wantArgs[i])
		}
	}
}

func TestWhois(t *testing.T) {
	const whois = `{
		"UserProfile": { "LoginName": "ibte@example.com" },
		"Node": { "Name": "laptop.tailnet-name.ts.net." }
	}`
	c := &Client{R: &fakeRunner{out: []byte(whois)}}
	got, err := c.Whois(context.Background(), "100.64.0.1")
	if err != nil {
		t.Fatalf("Whois: %v", err)
	}
	want := "ibte@example.com@laptop.tailnet-name.ts.net"
	if got != want {
		t.Errorf("Whois = %q, want %q", got, want)
	}
}

func assertCode(t *testing.T, err error, code tserr.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("want *tserr.Error %s, got nil", code)
	}
	var te *tserr.Error
	if !errors.As(err, &te) {
		t.Fatalf("want *tserr.Error, got %T: %v", err, err)
	}
	if te.Code != code {
		t.Errorf("code = %s, want %s", te.Code, code)
	}
}

func dnsNames(ps []Peer) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.DNSName
	}
	return out
}
