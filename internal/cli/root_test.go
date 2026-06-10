package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/version"
)

// run executes the root command with the given args against an in-memory
// buffer and returns captured stdout+stderr plus any error.
func run(args ...string) (string, error) {
	c := newRootCmd()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs(args)
	err := c.Execute()
	return buf.String(), err
}

func TestVersionMatchesEmbedded(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "9.9.9"

	out, err := run("--version")
	if err != nil {
		t.Fatalf("--version returned error: %v", err)
	}
	if want := "9.9.9\n"; out != want {
		t.Fatalf("--version = %q, want %q", out, want)
	}
}

func TestHelpListsAllCommands(t *testing.T) {
	out, err := run("--help")
	if err != nil {
		t.Fatalf("--help returned error: %v", err)
	}
	for _, cmd := range []string{
		"setup", "init", "location", "track", "status",
		"push", "pull", "gc", "verify", "revert",
	} {
		if !strings.Contains(out, cmd) {
			t.Errorf("--help output missing command %q\n%s", cmd, out)
		}
	}
}

func TestStubsRunCleanly(t *testing.T) {
	// Commands still backed by the notImplemented stub. As each command is
	// implemented it is removed from this list (track -> task-12,
	// location -> task-10, setup -> task-11, status -> task-13,
	// push/pull -> task-14/15 done). init/gc/verify/revert land in WS-C.
	cmds := [][]string{
		{"init"}, {"gc"}, {"verify"},
		{"revert", "a/b.pdf", "deadbeef"},
	}
	for _, args := range cmds {
		out, err := run(args...)
		if err != nil {
			t.Errorf("%v returned error: %v", args, err)
		}
		if !strings.Contains(out, "not implemented yet") {
			t.Errorf("%v output = %q, want it to contain %q", args, out, "not implemented yet")
		}
	}
}

func TestLocationAddRequiresArg(t *testing.T) {
	if _, err := run("location", "add"); err == nil {
		t.Fatal("location add with no arg should error (ExactArgs(1))")
	}
}

func TestRevertRequiresTwoArgs(t *testing.T) {
	if _, err := run("revert", "only-one"); err == nil {
		t.Fatal("revert with one arg should error (ExactArgs(2))")
	}
}
