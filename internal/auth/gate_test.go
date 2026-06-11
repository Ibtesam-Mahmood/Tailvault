package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func passwordFileOpts(t *testing.T, pw string) ReadOpts {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(p, []byte(pw), 0o600); err != nil {
		t.Fatal(err)
	}
	return ReadOpts{PasswordFile: p}
}

func TestGate_AcceptsCorrect(t *testing.T) {
	hf := mustHash(t, "open sesame")
	v := MemoryVerifier{HF: hf, Set: true}
	if err := Gate(context.Background(), v, passwordFileOpts(t, "open sesame")); err != nil {
		t.Errorf("Gate(correct) = %v, want nil", err)
	}
}

func TestGate_RejectsWrong(t *testing.T) {
	hf := mustHash(t, "open sesame")
	v := MemoryVerifier{HF: hf, Set: true}
	err := Gate(context.Background(), v, passwordFileOpts(t, "not it"))
	if !errors.Is(err, ErrWrongPassword) {
		t.Errorf("Gate(wrong) = %v, want ErrWrongPassword", err)
	}
}

func TestGate_NoPasswordSet(t *testing.T) {
	v := MemoryVerifier{Set: false}
	err := Gate(context.Background(), v, passwordFileOpts(t, "anything"))
	if !errors.Is(err, ErrNoPassword) {
		t.Errorf("Gate(no password set) = %v, want ErrNoPassword", err)
	}
}

func TestGate_NoPasswordSource(t *testing.T) {
	unsetEnv(t, EnvPassword)
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// No file, no env, non-TTY stdin: hard-fail before touching the verifier.
	v := &countingVerifier{}
	err = Gate(context.Background(), v, ReadOpts{Stdin: f})
	if !errors.Is(err, ErrNoPasswordSource) {
		t.Errorf("Gate(no source) = %v, want ErrNoPasswordSource", err)
	}
	if v.calls != 0 {
		t.Errorf("verifier called %d times despite no password source; want 0", v.calls)
	}
}

type countingVerifier struct{ calls int }

func (c *countingVerifier) VerifyPassword(context.Context, []byte) (bool, error) {
	c.calls++
	return true, nil
}
