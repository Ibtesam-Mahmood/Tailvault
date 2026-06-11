package cli

import (
	"bytes"
	"context"
	"strings"

	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// okTSRunner stubs `tailscale status --json` with a healthy, logged-in session so
// preflightNode/whoisSelf pass without a real tailnet — the 7b captured-status
// affordance shared by every preflight-requiring Block-4 row (gc/push/pull/
// status). Inject via `newTSClient = func() *tailscale.Client { return
// &tailscale.Client{R: okTSRunner{}} }` + cleanup. Ping is unused (reachability
// gates are overridden per-command, e.g. SetTestGCProbe).
type okTSRunner struct{}

func (okTSRunner) Run(_ context.Context, _ ...string) ([]byte, error) {
	return []byte(`{"BackendState":"Running","Self":{"DNSName":"me.ts.net","Online":true,"TailscaleIPs":["100.1.1.1"]}}`), nil
}

// execCLI drives the REAL root Cobra command end-to-end with argv exactly as a
// user types `tailvault …`, and maps the outcome to the bucketed PROCESS EXIT
// CODE through the SAME tserr.ExitCodeFor path cmd/tailvault/main.go uses (0 on
// success). This is the 7b bar for the Block-4 suite (TestBlock4_*): assert real
// argv + real bucketed exit codes — never just a returned error — and on-disk
// state. Returns combined stdout+stderr.
//
// Exit buckets (proposal / tserr): 0 ok · 2 TV-CFG / TV-AUTH-01 · 4 TV-NODE ·
// 5 TV-OBJ · 6 TV-FED. It also returns the underlying error so a test can pin the
// SPECIFIC tserr.Code (the root command silences errors, so the TV-* code is not
// in the buffer) — assert BOTH the bucketed exit code (7b) and the code
// (non-vacuity), e.g. exit 2 AND TV-AUTH-01 to distinguish it from TV-CFG-01.
func execCLI(args ...string) (out string, exit int, err error) {
	c := newRootCmd()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs(args)
	err = c.Execute()
	return buf.String(), tserr.ExitCodeFor(err), err
}

// execCLIStdin is execCLI with a piped stdin (for confirm prompts / non-TTY).
func execCLIStdin(stdin string, args ...string) (out string, exit int, err error) {
	c := newRootCmd()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetIn(strings.NewReader(stdin))
	c.SetArgs(args)
	err = c.Execute()
	return buf.String(), tserr.ExitCodeFor(err), err
}

// Bucketed process exit codes asserted across the Block-4 suite.
const (
	exitOK   = 0 // success
	exitCfg  = 2 // TV-CFG-01 / TV-AUTH-01
	exitNode = 4 // TV-NODE-01/02
	exitObj  = 5 // TV-OBJ-01
	exitFed  = 6 // TV-FED-01/02/03/04
)
