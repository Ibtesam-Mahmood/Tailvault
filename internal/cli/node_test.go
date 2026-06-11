package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/auth"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// runStdin executes the root command with stdin wired to in.
func runStdin(in string, args ...string) (string, error) {
	c := newRootCmd()
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetIn(bytes.NewReader([]byte(in)))
	c.SetArgs(args)
	err := c.Execute()
	return buf.String(), err
}

// writeVaultPasswd sets up a node vault dir with a password hash file.
func writeVaultPasswd(t *testing.T, password string) string {
	t.Helper()
	base := t.TempDir()
	hf, err := auth.NewHashFile([]byte(password))
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.WriteHashFile(auth.HashFilePath(base), hf); err != nil {
		t.Fatal(err)
	}
	return base
}

func TestNodeVerifyPasswd_Match(t *testing.T) {
	base := writeVaultPasswd(t, "correct horse")
	if _, err := runStdin("correct horse", "node", "verify-passwd", "--vault", base); err != nil {
		t.Errorf("verify-passwd(correct) = %v, want nil (exit 0)", err)
	}
}

func TestNodeVerifyPasswd_Rejected(t *testing.T) {
	base := writeVaultPasswd(t, "correct horse")
	_, err := runStdin("wrong", "node", "verify-passwd", "--vault", base)
	assertAuth(t, err, "rejected")
}

func TestNodeVerifyPasswd_NoPasswordSet(t *testing.T) {
	base := t.TempDir() // no hash file written
	_, err := runStdin("anything", "node", "verify-passwd", "--vault", base)
	assertAuth(t, err, "no password set")
	if !errors.Is(err, auth.ErrNoPassword) {
		t.Errorf("no-password-set should wrap ErrNoPassword; got %v", err)
	}
}

func TestNodeVerifyPasswd_CorruptHashFile(t *testing.T) {
	base := t.TempDir()
	p := auth.HashFilePath(base)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("garbage not a phc string"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runStdin("anything", "node", "verify-passwd", "--vault", base)
	assertAuth(t, err, "corrupt hash file")
}

func TestNodeVerifyPasswd_RequiresVault(t *testing.T) {
	_, err := runStdin("pw", "node", "verify-passwd")
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.ConfigBad {
		t.Errorf("missing --vault = %v, want TV-CFG-01", err)
	}
}

func TestNodeVerifyPasswd_Hidden(t *testing.T) {
	// The node group (and its verify-passwd subcommand) must be hidden from the
	// user-facing surface — it is an over-SSH internal helper.
	root := newRootCmd()
	var node *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "node" {
			node = c
			break
		}
	}
	if node == nil {
		t.Fatal("node group not registered")
	}
	if !node.Hidden {
		t.Error("node group must be Hidden")
	}
	vp, _, err := node.Find([]string{"verify-passwd"})
	if err != nil || vp == nil {
		t.Fatalf("verify-passwd subcommand not found: %v", err)
	}
	if !vp.Hidden {
		t.Error("node verify-passwd must be Hidden")
	}
}

func assertAuth(t *testing.T, err error, what string) {
	t.Helper()
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.AuthRequired {
		t.Errorf("%s: err = %v, want TV-AUTH-01", what, err)
		return
	}
	if te.ExitCode() != 2 {
		t.Errorf("%s: exit = %d, want 2", what, te.ExitCode())
	}
}
