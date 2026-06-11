package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadPassword_File(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(p, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPassword(ReadOpts{PasswordFile: p})
	if err != nil {
		t.Fatalf("ReadPassword: %v", err)
	}
	if string(got) != "s3cret" {
		t.Errorf("got %q, want %q (one trailing newline stripped)", got, "s3cret")
	}
}

func TestReadPassword_FilePreservesInnerSpaces(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(p, []byte("pass phrase with spaces"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPassword(ReadOpts{PasswordFile: p})
	if err != nil {
		t.Fatalf("ReadPassword: %v", err)
	}
	if string(got) != "pass phrase with spaces" {
		t.Errorf("got %q; spaces must be preserved", got)
	}
}

func TestReadPassword_Env(t *testing.T) {
	t.Setenv(EnvPassword, "from-env")
	got, err := ReadPassword(ReadOpts{})
	if err != nil {
		t.Fatalf("ReadPassword: %v", err)
	}
	if string(got) != "from-env" {
		t.Errorf("got %q, want from-env", got)
	}
}

func TestReadPassword_FileBeatsEnv(t *testing.T) {
	t.Setenv(EnvPassword, "from-env")
	p := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(p, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPassword(ReadOpts{PasswordFile: p})
	if err != nil {
		t.Fatalf("ReadPassword: %v", err)
	}
	if string(got) != "from-file" {
		t.Errorf("got %q, want from-file (file precedence)", got)
	}
}

func TestReadPassword_NoSourceNonTTY(t *testing.T) {
	unsetEnv(t, EnvPassword)
	// A regular file as stdin is not a terminal.
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = ReadPassword(ReadOpts{Stdin: f})
	if !errors.Is(err, ErrNoPasswordSource) {
		t.Errorf("non-TTY with no env/file: got %v, want ErrNoPasswordSource", err)
	}
}

func TestMemoryVerifier(t *testing.T) {
	ctx := context.Background()
	hf := mustHash(t, "harness pw")

	ok, err := MemoryVerifier{HF: hf, Set: true}.VerifyPassword(ctx, []byte("harness pw"))
	if err != nil || !ok {
		t.Errorf("correct password: ok %v err %v; want true, nil", ok, err)
	}
	ok, err = MemoryVerifier{HF: hf, Set: true}.VerifyPassword(ctx, []byte("wrong"))
	if err != nil || ok {
		t.Errorf("wrong password: ok %v err %v; want false, nil", ok, err)
	}
	_, err = MemoryVerifier{Set: false}.VerifyPassword(ctx, []byte("anything"))
	if !errors.Is(err, ErrNoPassword) {
		t.Errorf("no password set: got %v, want ErrNoPassword", err)
	}
}

// unsetEnv removes key for the duration of the test, restoring it after.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	os.Unsetenv(key)
	t.Cleanup(func() {
		if had {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
}
