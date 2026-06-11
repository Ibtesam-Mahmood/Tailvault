package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeriveVerify_RoundTrip(t *testing.T) {
	hf, err := NewHashFile([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatalf("NewHashFile: %v", err)
	}
	if !Verify(hf, []byte("correct horse battery staple")) {
		t.Error("Verify rejected the correct password")
	}
	if Verify(hf, []byte("Correct Horse Battery Staple")) {
		t.Error("Verify accepted a wrong password")
	}
	if Verify(hf, []byte("")) {
		t.Error("Verify accepted an empty password")
	}
}

func TestNewHashFile_FrozenParams(t *testing.T) {
	hf, err := NewHashFile([]byte("pw"))
	if err != nil {
		t.Fatalf("NewHashFile: %v", err)
	}
	if hf.Version != 19 || hf.MemoryKB != 65536 || hf.Time != 3 || hf.Threads != 4 {
		t.Errorf("params = v%d m%d t%d p%d; want v19 m65536 t3 p4",
			hf.Version, hf.MemoryKB, hf.Time, hf.Threads)
	}
	if len(hf.Salt) != 16 {
		t.Errorf("salt len = %d, want 16", len(hf.Salt))
	}
	if len(hf.Hash) != 32 {
		t.Errorf("hash len = %d, want 32", len(hf.Hash))
	}
}

func TestVerify_UsesStoredParamsNotDefaults(t *testing.T) {
	// A hash file written with NON-default params must still verify, proving
	// Verify reads params from the file (the SPEC v2 §16 gotcha).
	p := Params{Time: 1, MemoryKB: 8 * 1024, Threads: 1, KeyLen: 32}
	salt := []byte("0123456789abcdef")
	hf := HashFile{Version: 19, Time: p.Time, MemoryKB: p.MemoryKB, Threads: p.Threads, Salt: salt, Hash: Derive([]byte("pw"), salt, p)}
	if !Verify(hf, []byte("pw")) {
		t.Error("Verify failed to honor stored non-default params")
	}
}

func TestVerify_ZeroHashNeverAccepts(t *testing.T) {
	if Verify(HashFile{Salt: []byte("s")}, []byte("anything")) {
		t.Error("a zero-length stored hash must never accept")
	}
}

func TestFormatPHC_CanonicalShape(t *testing.T) {
	hf, _ := NewHashFile([]byte("pw"))
	s := FormatPHC(hf)
	if !strings.HasPrefix(s, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("PHC prefix wrong: %q", s)
	}
	if strings.Contains(s, "=") != strings.Contains("$argon2id$v=19$m=65536,t=3,p=4$", "=") {
		// the only '=' allowed are the literal v=/m=/t=/p= in the params header,
		// never base64 padding.
		if strings.Count(s, "=") != 4 {
			t.Errorf("unexpected '=' count (base64 must be unpadded): %q", s)
		}
	}
}

func TestParsePHC_RoundTrip(t *testing.T) {
	hf, _ := NewHashFile([]byte("round trip"))
	got, err := ParsePHC(FormatPHC(hf))
	if err != nil {
		t.Fatalf("ParsePHC: %v", err)
	}
	if got.Version != hf.Version || got.Time != hf.Time || got.MemoryKB != hf.MemoryKB || got.Threads != hf.Threads ||
		string(got.Salt) != string(hf.Salt) || string(got.Hash) != string(hf.Hash) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, hf)
	}
	// A parsed hash still verifies the original password.
	if !Verify(got, []byte("round trip")) {
		t.Error("parsed hash failed to verify the password")
	}
}

func TestParsePHC_Rejects(t *testing.T) {
	good := FormatPHC(mustHash(t, "pw"))
	for name, in := range map[string]string{
		"empty":           "",
		"no leading $":    strings.TrimPrefix(good, "$"),
		"wrong algorithm": strings.Replace(good, "argon2id", "argon2i", 1),
		"too few fields":  "$argon2id$v=19$m=65536,t=3,p=4$onlysalt",
		"bad params":      "$argon2id$v=19$m=x,t=3,p=4$c2FsdA$aGFzaA",
		"bad version":     "$argon2id$v=zz$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		"bad salt b64":    "$argon2id$v=19$m=65536,t=3,p=4$!!!$aGFzaA",
		"padded base64":   "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA==$aGFzaA==",
	} {
		if _, err := ParsePHC(in); err == nil {
			t.Errorf("%s: ParsePHC(%q) = nil error; want error", name, in)
		}
	}
}

func TestWriteLoadHashFile_RoundTrip(t *testing.T) {
	hf := mustHash(t, "disk pw")
	p := filepath.Join(t.TempDir(), "meta", "auth", "passwd")
	if err := WriteHashFile(p, hf); err != nil {
		t.Fatalf("WriteHashFile: %v", err)
	}
	// 0600 perms on the secret.
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("passwd perms = %o, want 600", fi.Mode().Perm())
	}
	got, ok, err := LoadHashFile(p)
	if err != nil || !ok {
		t.Fatalf("LoadHashFile = ok %v, err %v; want true, nil", ok, err)
	}
	if !Verify(got, []byte("disk pw")) {
		t.Error("loaded hash failed to verify password")
	}
}

func TestLoadHashFile_NoPasswordSet(t *testing.T) {
	_, ok, err := LoadHashFile(filepath.Join(t.TempDir(), "nope"))
	if ok || err != nil {
		t.Errorf("missing file = ok %v, err %v; want false, nil (no password set)", ok, err)
	}
}

func TestLoadHashFile_Corrupt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(p, []byte("not a phc string\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := LoadHashFile(p); err == nil || ok {
		t.Errorf("corrupt file = ok %v, err %v; want false, error (never a false accept)", ok, err)
	}
}

func TestHashFilePath(t *testing.T) {
	if got := HashFilePath("/mnt/ssd/tailvault/root-pnp"); got != "/mnt/ssd/tailvault/root-pnp/meta/auth/passwd" {
		t.Errorf("HashFilePath = %q", got)
	}
}

func mustHash(t *testing.T, pw string) HashFile {
	t.Helper()
	hf, err := NewHashFile([]byte(pw))
	if err != nil {
		t.Fatalf("NewHashFile: %v", err)
	}
	return hf
}
