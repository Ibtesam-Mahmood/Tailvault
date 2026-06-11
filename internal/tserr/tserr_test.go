package tserr

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCodeBuckets(t *testing.T) {
	cases := []struct {
		code Code
		want int
	}{
		{NetNotRunning, 3},
		{NetNotLoggedIn, 3},
		{NodeOffline, 4},
		{NodeNotWritable, 4},
		{ObjMissing, 5},
		{ConfigBad, 2},             // config/precondition bucket
		{Code("TV-UNKNOWN-99"), 2}, // unmapped code fails safe as precondition
	}
	for _, c := range cases {
		e := &Error{Code: c.code, Cause: "x", Fix: "y"}
		if got := e.ExitCode(); got != c.want {
			t.Errorf("code %s: ExitCode() = %d, want %d", c.code, got, c.want)
		}
	}
}

func TestErrorFormatting(t *testing.T) {
	e := NetNotRunningErr(nil)
	want := "TV-NET-01: Tailscale not running (fix: start Tailscale and run `tailscale status`)"
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestConstructorsInterpolate(t *testing.T) {
	if got := NodeOfflineErr("home-pi", nil).Error(); got !=
		"TV-NODE-01: storage node \"home-pi\" is offline/unreachable (fix: check the node is powered on and connected; run `tailvault location ls`)" {
		t.Errorf("NodeOfflineErr text drift: %q", got)
	}
	if got := ObjMissingErr("9f2b1c", nil).Error(); got !=
		"TV-OBJ-01: expected blob 9f2b1c missing on the node (fix: re-push from a clone that has it, or run `tailvault verify`)" {
		t.Errorf("ObjMissingErr text drift: %q", got)
	}
}

func TestExitCodeFor(t *testing.T) {
	if got := ExitCodeFor(nil); got != 0 {
		t.Errorf("ExitCodeFor(nil) = %d, want 0", got)
	}
	if got := ExitCodeFor(errors.New("boom")); got != 1 {
		t.Errorf("ExitCodeFor(untyped) = %d, want 1", got)
	}
	wrapped := fmt.Errorf("push failed: %w", NodeOfflineErr("home-pi", nil))
	if got := ExitCodeFor(wrapped); got != 4 {
		t.Errorf("ExitCodeFor(wrapped TV-NODE-01) = %d, want 4", got)
	}
}

func TestUnwrap(t *testing.T) {
	underlying := errors.New("dial tcp: refused")
	e := NetNotRunningErr(underlying)
	if !errors.Is(e, underlying) {
		t.Error("errors.Is should find the wrapped underlying error")
	}
}
