package filter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/pointer"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

func testEnv(t *testing.T) *Env {
	t.Helper()
	return &Env{
		Cfg: &config.Config{
			Version: 1,
			Storage: config.Storage{Location: "home-pi"},
			Rules: config.Rules{
				MinSize: "5MB",
				Include: []string{"**/*.pdf"},
				Exclude: []string{"drafts/**"},
			},
		},
		Backend: backend.NewFSBackend(t.TempDir()),
	}
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestClean_ManagedEmitsPointer_RoundTrip(t *testing.T) {
	env := testEnv(t)
	ctx := context.Background()
	// Arbitrary binary content, smaller than min_size but matched by *.pdf glob.
	content := []byte{0x25, 0x50, 0x44, 0x46, 0x00, 0xFF, 0xD8, 0xFF, 0x01, 0x02}

	var cleaned bytes.Buffer
	if err := Clean(ctx, env, "docs/board.pdf", bytes.NewReader(content), &cleaned); err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if !pointer.IsPointer(cleaned.Bytes()) {
		t.Fatalf("Clean did not emit a pointer:\n%s", cleaned.Bytes())
	}
	p, err := pointer.Decode(cleaned.Bytes())
	if err != nil {
		t.Fatalf("decode emitted pointer: %v", err)
	}
	if p.SHA256 != sha256hex(content) {
		t.Errorf("pointer sha = %s, want %s", p.SHA256, sha256hex(content))
	}
	if p.Size != int64(len(content)) {
		t.Errorf("pointer size = %d, want %d", p.Size, len(content))
	}
	if p.Location != "home-pi" {
		t.Errorf("pointer location = %q, want home-pi", p.Location)
	}

	// Seed the backend with the real blob (push would have done this), then smudge.
	if err := env.Backend.Put(ctx, "objects/"+p.SHA256, bytes.NewReader(content)); err != nil {
		t.Fatalf("seed blob: %v", err)
	}
	var restored bytes.Buffer
	if err := Smudge(ctx, env, bytes.NewReader(cleaned.Bytes()), &restored); err != nil {
		t.Fatalf("Smudge: %v", err)
	}
	if !bytes.Equal(restored.Bytes(), content) {
		t.Errorf("round-trip mismatch:\n got %x\nwant %x", restored.Bytes(), content)
	}
}

func TestClean_NonManagedPassThrough(t *testing.T) {
	env := testEnv(t)
	// Excluded by drafts/**, even though it ends in .pdf.
	binary := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46}
	var out bytes.Buffer
	if err := Clean(context.Background(), env, "drafts/wip.pdf", bytes.NewReader(binary), &out); err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if !bytes.Equal(out.Bytes(), binary) {
		t.Errorf("non-managed clean altered bytes:\n got %x\nwant %x", out.Bytes(), binary)
	}
	// A small, unmatched text file is also pass-through.
	out.Reset()
	txt := []byte("just a small note\n")
	if err := Clean(context.Background(), env, "notes.txt", bytes.NewReader(txt), &out); err != nil {
		t.Fatalf("Clean txt: %v", err)
	}
	if !bytes.Equal(out.Bytes(), txt) {
		t.Errorf("small txt altered: got %q want %q", out.Bytes(), txt)
	}
}

func TestSmudge_NonPointerPassThrough(t *testing.T) {
	env := testEnv(t)
	// Random binary that is not a pointer (no magic line).
	binary := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0x80, 0x7F, 0x0A, 0x0A}
	var out bytes.Buffer
	if err := Smudge(context.Background(), env, bytes.NewReader(binary), &out); err != nil {
		t.Fatalf("Smudge: %v", err)
	}
	if !bytes.Equal(out.Bytes(), binary) {
		t.Errorf("non-pointer smudge altered bytes:\n got %x\nwant %x", out.Bytes(), binary)
	}
}

func TestSmudge_MissingBlob(t *testing.T) {
	env := testEnv(t)
	content := []byte("missing blob content")
	p := pointer.Pointer{SHA256: sha256hex(content), Size: int64(len(content)), Location: "home-pi"}
	// Backend is empty -> Get returns TV-OBJ-01.
	var out bytes.Buffer
	err := Smudge(context.Background(), env, bytes.NewReader(pointer.Encode(p)), &out)
	if err == nil {
		t.Fatal("expected error for missing blob")
	}
	var te *tserr.Error
	if !errors.As(err, &te) || te.Code != tserr.ObjMissing {
		t.Errorf("want TV-OBJ-01 tserr.Error, got %v", err)
	}
	if te.ExitCode() != 5 {
		t.Errorf("exit code = %d, want 5", te.ExitCode())
	}
	if !errors.Is(err, backend.ErrNotExist) {
		t.Errorf("missing-blob error should wrap backend.ErrNotExist")
	}
	if out.Len() != 0 {
		t.Errorf("smudge wrote %d bytes on missing blob; must write nothing", out.Len())
	}
}

func TestSmudge_IntegrityMismatch(t *testing.T) {
	env := testEnv(t)
	ctx := context.Background()
	content := []byte("the real content")
	p := pointer.Pointer{SHA256: sha256hex(content), Size: int64(len(content)), Location: "home-pi"}
	// Plant a mis-keyed blob: stored under the right key but with wrong bytes.
	corrupt := []byte("CORRUPTED bytes that do not hash to the key")
	if err := env.Backend.Put(ctx, "objects/"+p.SHA256, bytes.NewReader(corrupt)); err != nil {
		t.Fatalf("plant corrupt blob: %v", err)
	}
	var out bytes.Buffer
	err := Smudge(ctx, env, bytes.NewReader(pointer.Encode(p)), &out)
	if err == nil {
		t.Fatal("expected integrity error for mis-keyed blob")
	}
	var te *tserr.Error
	if !errors.As(err, &te) || te.ExitCode() != 5 {
		t.Errorf("want integrity tserr.Error exit 5, got %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("smudge wrote %d bytes on integrity failure; must write nothing", out.Len())
	}
}
