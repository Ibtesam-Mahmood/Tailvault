// Package tailscale wraps the local `tailscale` CLI. tailvault deliberately
// carries almost no networking code: addressing, liveness, and identity all
// ride on Tailscale primitives. This package shells out to the `tailscale`
// binary and parses its output. It reads ONLY the local, already-authenticated
// session — it performs no login and stores no credentials.
package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"sort"
	"strings"

	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// Peer is the subset of a tailscale status peer tailvault cares about.
type Peer struct {
	DNSName string // MagicDNS name, trailing dot trimmed, e.g. "home-pi.tailnet-name.ts.net"
	Online  bool
}

// Status is the parsed, trimmed view of `tailscale status --json`.
type Status struct {
	Self     Peer
	Peers    []Peer
	LoggedIn bool
}

// Runner indirects exec so tests inject canned output. The default shells to
// the `tailscale` binary on PATH.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// Client is the typed wrapper over the tailscale CLI.
type Client struct{ R Runner }

// New returns a Client that shells out to the real `tailscale` binary.
func New() *Client { return &Client{R: execRunner{}} }

// execRunner is the default Runner: it invokes `tailscale <args...>`.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "tailscale", args...).Output()
}

// statusJSON mirrors only the few fields tailvault needs out of the large,
// version-dependent `tailscale status --json` document. Unknown keys are
// ignored (default encoding/json behaviour).
type statusJSON struct {
	BackendState string              `json:"BackendState"`
	Self         *peerJSON           `json:"Self"`
	Peer         map[string]peerJSON `json:"Peer"`
}

type peerJSON struct {
	DNSName string `json:"DNSName"`
	Online  bool   `json:"Online"`
}

// Status parses `tailscale status --json`.
//
// A missing binary or an unreachable daemon maps to TV-NET-01; a logged-out
// session (BackendState != "Running") maps to TV-NET-02. Parsing, not guessing,
// distinguishes the two.
func (c *Client) Status(ctx context.Context) (Status, error) {
	out, err := c.R.Run(ctx, "status", "--json")
	if err != nil {
		if isBinaryMissing(err) {
			return Status{}, tserr.NetNotRunningErr(err)
		}
		// A non-zero exit with no parseable JSON means the daemon is not
		// answering (down / not reachable): TV-NET-01.
		var sj statusJSON
		if json.Unmarshal(out, &sj) != nil {
			return Status{}, tserr.NetNotRunningErr(err)
		}
		// JSON parsed despite the error — fall through to inspect BackendState.
		return projectStatus(sj)
	}

	var sj statusJSON
	if uerr := json.Unmarshal(out, &sj); uerr != nil {
		// Output that does not parse at all is treated as the daemon not being
		// reachable rather than a silent success.
		return Status{}, tserr.NetNotRunningErr(uerr)
	}
	return projectStatus(sj)
}

// projectStatus validates BackendState and projects the raw JSON into the
// trimmed public model.
func projectStatus(sj statusJSON) (Status, error) {
	if sj.BackendState != "Running" {
		return Status{LoggedIn: false}, tserr.NetNotLoggedInErr(nil)
	}
	st := Status{LoggedIn: true}
	if sj.Self != nil {
		st.Self = Peer{DNSName: trimDNS(sj.Self.DNSName), Online: sj.Self.Online}
	}
	for _, p := range sj.Peer {
		st.Peers = append(st.Peers, Peer{DNSName: trimDNS(p.DNSName), Online: p.Online})
	}
	// Deterministic ordering for stable pick-lists and tests.
	sort.Slice(st.Peers, func(i, j int) bool { return st.Peers[i].DNSName < st.Peers[j].DNSName })
	return st, nil
}

// Ping shells `tailscale ping --c 1 <node>`. A non-zero exit / timeout is
// returned as a plain error; node-level mapping to TV-NODE-01 is the
// backend/preflight's job (task-09), not this package's.
func (c *Client) Ping(ctx context.Context, node string) error {
	_, err := c.R.Run(ctx, "ping", "--c", "1", node)
	return err
}

// whoisJSON mirrors the fields of `tailscale whois --json <addr>` needed to
// build the "user@host" pusher stamp.
type whoisJSON struct {
	UserProfile *struct {
		LoginName string `json:"LoginName"`
	} `json:"UserProfile"`
	Node *struct {
		Name string `json:"Name"`
	} `json:"Node"`
}

// Whois shells `tailscale whois --json <addr>` and resolves a tailnet identity
// as "user@host" for the pusher stamp written into tailvault.lock.
func (c *Client) Whois(ctx context.Context, addr string) (string, error) {
	out, err := c.R.Run(ctx, "whois", "--json", addr)
	if err != nil {
		return "", err
	}
	var wj whoisJSON
	if uerr := json.Unmarshal(out, &wj); uerr != nil {
		return "", uerr
	}
	user := ""
	if wj.UserProfile != nil {
		user = wj.UserProfile.LoginName
	}
	host := ""
	if wj.Node != nil {
		host = trimDNS(wj.Node.Name)
	}
	return user + "@" + host, nil
}

// trimDNS removes the trailing dot from a MagicDNS name so callers get
// "home-pi.tailnet.ts.net" rather than "home-pi.tailnet.ts.net.".
func trimDNS(s string) string { return strings.TrimSuffix(s, ".") }

// isBinaryMissing reports whether err indicates the `tailscale` binary is
// absent from PATH (as opposed to a binary that ran and exited non-zero).
func isBinaryMissing(err error) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	var ee *exec.Error // wraps ErrNotFound from LookPath inside CommandContext
	if errors.As(err, &ee) {
		return errors.Is(ee.Err, exec.ErrNotFound)
	}
	return false
}
