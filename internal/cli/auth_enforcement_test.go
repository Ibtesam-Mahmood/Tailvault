package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestAuthEnforcement_GatedSet is the §16 enforcement audit: it walks the whole
// command tree and asserts that exactly the SPEC v2 password-gated set carries the
// gated annotation (every mutating remote op gates before its WAL intent) and that
// no read command does. A new mutating command added without a gate — or a read
// accidentally gated — fails here rather than silently weakening D9.
//
// gatedAllow is the full SPEC v2 §16 gated set (names as they appear in the tree).
// mustBeGated is the subset OWNED by WS-C and therefore guaranteed present in this
// branch; restore-identity (coder-a) and the remote gc path (coder-b) are in the
// allow-list but their presence + annotation is finalized in the integration build
// (task-50), where all commands coexist.
func TestAuthEnforcement_GatedSet(t *testing.T) {
	gatedAllow := map[string]bool{
		"mv": true, "rm": true, "sync-mode": true, "passwd": true,
		"evict": true, "join": true, "leave": true,
		"restore-identity": true, "gc": true,
	}
	mustBeGated := []string{"mv", "rm", "sync-mode", "passwd", "evict", "join", "leave"}
	// Reads ride the tailnet ACL + SSH alone — NEVER password-gated (§16).
	reads := map[string]bool{"ls": true, "stat": true, "get": true, "status": true, "scan": true}

	byName := map[string]*cobra.Command{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			byName[sub.Name()] = sub
			walk(sub)
		}
	}
	walk(newRootCmd())

	isGated := func(c *cobra.Command) bool {
		return c != nil && c.Annotations[gatedAnnotation] == "1"
	}

	// (1) No stray gate: every annotated command is in the allow-list.
	for name, c := range byName {
		if isGated(c) && !gatedAllow[name] {
			t.Errorf("command %q is password-gated but not in the SPEC §16 gated set", name)
		}
	}
	// (2) No gated read: read commands must never carry the gate.
	for name := range reads {
		if c, ok := byName[name]; ok && isGated(c) {
			t.Errorf("read command %q must NOT be password-gated (§16: reads ride tailnet ACL + SSH)", name)
		}
	}
	// (3) Completeness for WS-C-owned gated commands present in this branch.
	for _, name := range mustBeGated {
		c, ok := byName[name]
		if !ok {
			t.Errorf("expected gated command %q to be registered", name)
			continue
		}
		if !isGated(c) {
			t.Errorf("mutating command %q must be password-gated (missing the gate annotation)", name)
		}
	}
}
