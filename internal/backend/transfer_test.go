package backend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// writeObj seeds objects/<key> under a taildrive root.
func writeObj(t *testing.T, root, key string, content []byte) {
	t.Helper()
	p := filepath.Join(root, "objects", key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTransfer_TaildriveRootToRoot(t *testing.T) {
	ctx := context.Background()
	srcRoot, destRoot := t.TempDir(), t.TempDir()
	content := []byte("peer-to-peer bytes\n")
	writeObj(t, srcRoot, "abc123", content)

	src := NewTaildrive(srcRoot)
	dest := NewTaildrive(destRoot)
	if err := Transfer(ctx, src, dest, "objects/abc123"); err != nil {
		t.Fatalf("transfer: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destRoot, "objects", "abc123"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("dest object = %q err=%v, want %q", got, err, content)
	}
	// Source must be untouched (the move command demotes the source separately).
	if _, err := os.Stat(filepath.Join(srcRoot, "objects", "abc123")); err != nil {
		t.Errorf("source object should remain after transfer: %v", err)
	}
	// Idempotent: a second transfer is a content-addressed no-op, not an error.
	if err := Transfer(ctx, src, dest, "objects/abc123"); err != nil {
		t.Errorf("second transfer should dedup cleanly: %v", err)
	}
}

func TestTransfer_MissingSourceObject(t *testing.T) {
	ctx := context.Background()
	src := NewTaildrive(t.TempDir())
	dest := NewTaildrive(t.TempDir())
	err := Transfer(ctx, src, dest, "objects/nope")
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.ObjMissing {
		t.Fatalf("missing source: want TV-OBJ-01, got %v", err)
	}
}

func TestTransfer_NoPeerPathRefused(t *testing.T) {
	// A taildrive share cannot receive from a non-taildrive source: refused, never
	// silently relayed through the client.
	ctx := context.Background()
	srcSSH := &SSH{User: "ibte", Node: "home-pi", BasePath: "/mnt/tv"}
	dest := NewTaildrive(t.TempDir())
	if err := Transfer(ctx, srcSSH, dest, "objects/x"); err == nil {
		t.Fatal("transfer from ssh into a taildrive share should be refused")
	}
}

func TestTransfer_SSHNodeToNode(t *testing.T) {
	// The transfer command must run ON THE DEST node, reach back to the SOURCE
	// node, and never stream bytes through the client (stdin to the dest ssh is
	// nil). This proves the peer-to-peer property at the SSH layer.
	ctx := context.Background()
	var sawStdin bool
	r := &scriptedRunner{handle: func(_ string, in io.Reader, _ io.Writer) ([]byte, error) {
		if in != nil {
			if b, _ := io.ReadAll(in); len(b) > 0 {
				sawStdin = true
			}
		}
		return nil, nil
	}}
	src := &SSH{User: "ibte", Node: "src-node", BasePath: "/mnt/src"}
	dest := &SSH{User: "ibte", Node: "dest-node", BasePath: "/mnt/dest", R: r}

	if err := Transfer(ctx, src, dest, "objects/deadbeef"); err != nil {
		t.Fatalf("ssh node-to-node transfer: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("want exactly one remote command, got %d: %v", len(r.calls), r.calls)
	}
	cmd := r.calls[0]
	if !strings.Contains(cmd, "rsync") {
		t.Errorf("transfer command should prefer rsync: %q", cmd)
	}
	if !strings.Contains(cmd, "ibte@src-node") || !strings.Contains(cmd, "/mnt/src/objects/deadbeef") {
		t.Errorf("transfer command must reference the source node + path: %q", cmd)
	}
	if !strings.Contains(cmd, "/mnt/dest/objects/deadbeef") {
		t.Errorf("transfer command must reference the dest path: %q", cmd)
	}
	if sawStdin {
		t.Error("bytes were streamed through the client (stdin non-empty) — must be peer-to-peer")
	}
}
