package tserr

import (
	"errors"
	"strings"
	"testing"
)

func TestFedExitBucket6(t *testing.T) {
	cases := []struct {
		name string
		err  *Error
	}{
		{"partial-view", FedPartialViewErr("30092d830e26", []string{"pi-2"}, nil)},
		{"need-all-members", FedNeedAllMembersErr("gc", []string{"pi-2", "office-nas"}, nil)},
		{"chain-broken", FedChainBrokenErr("pi-2", nil)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.err.ExitCode(); got != 6 {
				t.Errorf("ExitCode = %d, want 6", got)
			}
			if got := ExitCodeFor(c.err); got != 6 {
				t.Errorf("ExitCodeFor = %d, want 6", got)
			}
		})
	}
}

func TestFedPartialViewErr_CarriesUnreachable(t *testing.T) {
	e := FedPartialViewErr("abc123", []string{"pi-2", "office-nas"}, nil)
	if e.Code != FedPartialView {
		t.Errorf("Code = %q, want TV-FED-01", e.Code)
	}
	msg := e.Error()
	if !strings.Contains(msg, "pi-2") || !strings.Contains(msg, "office-nas") {
		t.Errorf("message missing unreachable members: %q", msg)
	}
	if !strings.Contains(msg, "abc123") {
		t.Errorf("message missing file id: %q", msg)
	}
}

func TestFedNeedAllMembersErr_CarriesOpAndMembers(t *testing.T) {
	e := FedNeedAllMembersErr("gc", []string{"pi-2"}, nil)
	if e.Code != FedNeedAllMembers {
		t.Errorf("Code = %q, want TV-FED-02", e.Code)
	}
	if !strings.Contains(e.Error(), "gc") || !strings.Contains(e.Error(), "pi-2") {
		t.Errorf("message missing op/member: %q", e.Error())
	}
}

func TestFedChainBrokenErr_Unwrap(t *testing.T) {
	sentinel := errors.New("chain link 3 != 4")
	e := FedChainBrokenErr("pi-2", sentinel)
	if !errors.Is(e, sentinel) {
		t.Error("FedChainBrokenErr should wrap the underlying error")
	}
	if e.Code != FedChainBroken {
		t.Errorf("Code = %q, want TV-FED-03", e.Code)
	}
}

func TestJoinMembers_EmptyTolerant(t *testing.T) {
	// Defensive: constructors must not render an empty list as bare punctuation.
	e := FedPartialViewErr("id", nil, nil)
	if strings.Contains(e.Error(), "; unreachable") && !strings.Contains(e.Error(), "unknown member") {
		t.Errorf("empty unreachable list rendered poorly: %q", e.Error())
	}
}

// Existing v1 buckets must be unchanged by the v2 additions.
func TestV1BucketsUnchanged(t *testing.T) {
	if ConfigErr("bad", nil).ExitCode() != 2 {
		t.Error("ConfigErr should stay exit 2")
	}
	if ObjMissingErr("sha", nil).ExitCode() != 5 {
		t.Error("ObjMissingErr should stay exit 5")
	}
	if NodeOfflineErr("pi", nil).ExitCode() != 4 {
		t.Error("NodeOfflineErr should stay exit 4")
	}
}
