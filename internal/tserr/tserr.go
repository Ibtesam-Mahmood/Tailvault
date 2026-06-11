// Package tserr defines tailvault's structured error model: a small set of
// typed conditions, each with a stable code, a one-line cause, and a concrete
// fix, plus a bucketed exit-code mapping so scripts and the git pre-push hook
// can branch on why a command failed.
package tserr

import (
	"errors"
	"fmt"
	"strings"
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
	AuthRequired    Code = "TV-AUTH-01" // password missing/rejected on a mutating remote op

	// v2 federation codes (SPEC v2 §15) — all map to the new exit bucket 6.
	FedPartialView    Code = "TV-FED-01" // not found among reachable members; ≥1 unreachable ("cannot prove absence")
	FedNeedAllMembers Code = "TV-FED-02" // op needs ALL members (gc) but ≥1 was unreachable
	FedChainBroken    Code = "TV-FED-03" // WAL hash-chain verification failed (tamper/corruption)
	FedIDCollision    Code = "TV-FED-04" // an id is already live on another member (restore would duplicate identity)
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
//	4 node unreachable, 5 integrity/missing blob, 6 federation/partial view.
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
	case AuthRequired:
		// SPEC v2 §16: TV-AUTH-01 reuses bucket 2 (precondition/auth) — the op is
		// refused before any work, exactly like a config precondition. No new bucket.
		return 2
	case FedPartialView, FedNeedAllMembers, FedChainBroken, FedIDCollision:
		return 6 // federation / partial view (SPEC v2 §15)
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

// FedPartialViewErr reports that a file was not found among the reachable
// members while at least one member was unreachable — absence cannot be proven.
// This is the safety verdict that prevents a down member from degrading into a
// silent "missing". Maps to exit bucket 6 (SPEC v2 §15).
func FedPartialViewErr(id string, unreachable []string, err error) *Error {
	return &Error{
		Code:  FedPartialView,
		Cause: fmt.Sprintf("file %s not found among reachable members; %s unreachable (cannot prove absence)", id, joinMembers(unreachable)),
		Fix:   "bring the offline member(s) online and retry, or run `tailvault ops` / check the cache for last-known state",
		Err:   err,
	}
}

// FedNeedAllMembersErr reports that an all-members operation (gc) ran while at
// least one member was unreachable. Maps to exit bucket 6 (SPEC v2 §15, D27/R3).
func FedNeedAllMembersErr(op string, unreachable []string, err error) *Error {
	return &Error{
		Code:  FedNeedAllMembers,
		Cause: fmt.Sprintf("%s requires all federation members but %s unreachable", op, joinMembers(unreachable)),
		Fix:   "bring all members online and retry; deletes never tolerate partial views",
		Err:   err,
	}
}

// FedChainBrokenErr reports that a member's WAL hash-chain failed verification
// (tamper or corruption). Maps to exit bucket 6 (SPEC v2 §15).
func FedChainBrokenErr(node string, err error) *Error {
	return &Error{
		Code:  FedChainBroken,
		Cause: fmt.Sprintf("WAL hash-chain verification failed on node %q", node),
		Fix:   "inspect with the chain-verify tooling; restore the affected node's WAL from a clone/backup",
		Err:   err,
	}
}

// FedIDCollisionErr reports that a self-certifying id is already live on a
// federation member, so restoring it here would create two live claims to one
// identity (SPEC v2 §15; task-48 collision guard). Discovered by a resolution
// fan-out before any mutation. Maps to exit bucket 6 (federation). member is the
// member already holding the id (or this node, for a same-catalog duplicate).
func FedIDCollisionErr(id, member string, err error) *Error {
	return &Error{
		Code:  FedIDCollision,
		Cause: fmt.Sprintf("id %s is already live on member %q — restoring would create two live claims to one identity", id, member),
		Fix:   "this id is not lost; do not restore it here — resolve the existing claim first if it is the wrong one",
		Err:   err,
	}
}

// joinMembers renders an unreachable-member list for error text, tolerating an
// empty list (defensive — TV-FED-01/02 always carry ≥1 by construction).
func joinMembers(members []string) string {
	if len(members) == 0 {
		return "an unknown member"
	}
	return strings.Join(members, ", ")
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

// AuthErr reports that a mutating remote op was attempted without a valid
// password (none set, none supplied, or the supplied one was rejected). Maps to
// exit bucket 2; refused before any work. The cause is caller-supplied since the
// three situations read differently to the user; the fix always points at the
// no-recovery reset path (SSH/physical access).
func AuthErr(cause string, err error) *Error {
	return &Error{
		Code:  AuthRequired,
		Cause: cause,
		Fix:   "supply the node password, or reset the hash over SSH/physical access (no recovery)",
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
