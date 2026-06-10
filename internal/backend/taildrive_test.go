package backend

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// The taildrive backend must pass the identical contract the SSH/FS backends do.
func TestTaildriveBackend_Contract(t *testing.T) {
	RunContract(t, NewTaildrive(t.TempDir()))
}

func TestTaildrive_Dedup(t *testing.T) {
	ctx := context.Background()
	b := NewTaildrive(t.TempDir())
	const key = "objects/x"
	if err := b.Put(ctx, key, bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("Put #1: %v", err)
	}
	full := b.pathFor(key)
	fi1, _ := os.Stat(full)
	if err := b.Put(ctx, key, bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("Put #2: %v", err)
	}
	fi2, _ := os.Stat(full)
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Errorf("dedup failed: file rewritten (mtime changed)")
	}
}

func TestTaildrive_UnmountedShare_NodeError(t *testing.T) {
	// A root under a non-existent parent simulates an unmounted share: Get of a
	// key whose parent dir is absent returns TV-OBJ-01 (file missing), but a
	// permission failure maps to TV-NODE-02. Here we assert the unwritable case.
	dir := t.TempDir()
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil { // read+execute, no write
		t.Fatalf("mkdir ro: %v", err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o700) })
	b := NewTaildrive(filepath.Join(ro, "share"))

	err := b.Put(context.Background(), "objects/y", bytes.NewReader([]byte("d")))
	if err == nil {
		t.Skip("filesystem allowed the write (likely running as root); skipping perm assertion")
	}
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.NodeNotWritable {
		t.Errorf("Put to unwritable share = %v, want TV-NODE-02", err)
	}
}
