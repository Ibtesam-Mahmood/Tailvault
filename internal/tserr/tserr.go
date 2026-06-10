// Package tserr defines tailvault's structured error model: a small set of
// typed conditions, each with a stable code, a one-line cause, and a concrete
// fix, plus a bucketed exit-code mapping so scripts and the git pre-push hook
// can branch on why a command failed.
package tserr

import (
	"errors"
	"fmt"
)

// Code is a stable, documented error identifier surfaced to users and scripts.
type Code string

const (
	ConfigBad       Code = "TV-CFG-01"  // bad/missing tailvault.toml or precondition
	NetNotRunning   Code = "TV-NET-01"  // Tailscale not running / not in PATH
	NetNotLoggedIn  Code = "TV-NET-02"  // not logged into the tailnet
	NodeOffline     Code = "TV-NODE-01" // storage node offline/unreachable
	NodeNotWritable Code = "TV-NODE-02" // node reachable but base_path not writable
	ObjMissing      Code = "TV-OBJ-01"  // expected blob missing on the node
)

// Error is a typed tailvault failure: stable code, one-line cause, concrete fix.
type Error struct {
	Code  Code
	Cause string
	Fix   string
	Err   error // optional wrapped underlying error (for %w / debugging)
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s (fix: %s)", e.Code, e.Cause, e.Fix)
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCode maps a code into the proposal's bucketed process exit codes:
//
//	0 success, 2 config/precondition, 3 network/Tailscale,
//	4 node unreachable, 5 integrity/missing blob.
//
// The bucket numbers are a public contract (the pre-push hook reads them) — do
// not renumber.
func (e *Error) ExitCode() int {
	switch e.Code {
	case NetNotRunning, NetNotLoggedIn:
		return 3
	case NodeOffline, NodeNotWritable:
		return 4
	case ObjMissing:
		return 5
	default:
		return 2 // config/precondition fallback — any unmapped code fails safe
	}
}

// Helper constructors keep call sites terse and the user-facing cause/fix text
// consistent across packages. Only the variable bits (node name, sha) vary.

// ConfigErr reports a config/precondition failure (bad or missing
// tailvault.toml, unparseable input). Maps to exit bucket 2. The cause is
// caller-supplied since config problems are varied.
func ConfigErr(cause string, err error) *Error {
	return &Error{
		Code:  ConfigBad,
		Cause: cause,
		Fix:   "check tailvault.toml and re-run",
		Err:   err,
	}
}

// NetNotRunningErr reports that the local Tailscale daemon is unreachable.
func NetNotRunningErr(err error) *Error {
	return &Error{
		Code:  NetNotRunning,
		Cause: "Tailscale not running",
		Fix:   "start Tailscale and run `tailscale status`",
		Err:   err,
	}
}

// NetNotLoggedInErr reports that the machine is not logged into the tailnet.
func NetNotLoggedInErr(err error) *Error {
	return &Error{
		Code:  NetNotLoggedIn,
		Cause: "not logged into the tailnet",
		Fix:   "run `tailscale up`",
		Err:   err,
	}
}

// NodeOfflineErr reports that the named storage node is offline/unreachable.
func NodeOfflineErr(node string, err error) *Error {
	return &Error{
		Code:  NodeOffline,
		Cause: fmt.Sprintf("storage node %q is offline/unreachable", node),
		Fix:   "check the node is powered on and connected; run `tailvault location ls`",
		Err:   err,
	}
}

// NodeNotWritableErr reports that the node is reachable but base_path is not writable.
func NodeNotWritableErr(node string, err error) *Error {
	return &Error{
		Code:  NodeNotWritable,
		Cause: fmt.Sprintf("node %q reachable but base_path not writable", node),
		Fix:   "check the SSH user / Taildrive share and base_path permissions",
		Err:   err,
	}
}

// ObjMissingErr reports that an expected content-addressed blob is missing.
func ObjMissingErr(sha string, err error) *Error {
	return &Error{
		Code:  ObjMissing,
		Cause: fmt.Sprintf("expected blob %s missing on the node", sha),
		Fix:   "re-push from a clone that has it, or run `tailvault verify`",
		Err:   err,
	}
}

// ExitCodeFor converts any error into a process exit code at the main boundary:
// nil → 0; a *Error (even wrapped) → its bucket; any other error → 1 (generic).
func ExitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var e *Error
	if errors.As(err, &e) {
		return e.ExitCode()
	}
	return 1 // generic/unexpected failure
}
