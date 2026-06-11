// Package auth is tailvault's per-node password layer (D9, SPEC v2 §16). A node
// stores a single argon2id hash in canonical PHC string form at
// <base_path>/<subpath>/meta/auth/passwd (mode 0600); mutating remote ops
// (mv/rm/sync-mode/remote gc/evict/roster writes) require the matching password,
// while reads are never gated.
//
// We never roll our own crypto (D8): derivation is golang.org/x/crypto/argon2,
// randomness is crypto/rand, and the equality check is crypto/subtle. Verification
// always uses the parameters stored IN the hash file, never the package defaults,
// so a future parameter bump cannot lock out an existing node.
//
// This is a leaf package: it returns plain errors. The command boundary wraps a
// rejected/absent password as tserr.AuthErr (TV-AUTH-01, exit bucket 2) per the
// SPEC §8 error-layering rule.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/argon2"
)

// HashFileRel is the store-relative location of the password hash file, joined
// onto a node's <base_path>/<subpath> (SPEC v2 §16, §9 layout).
const HashFileRel = "meta/auth/passwd"

// Frozen argon2id parameters (SPEC v2 §16). These are the defaults used when a
// password is FIRST set; verification reads the params back from the stored hash
// file so old nodes keep working across a future bump.
const (
	argonVersion   = argon2.Version // 19 (0x13)
	defaultTime    = 3              // t
	defaultMemory  = 64 * 1024      // m, in KiB = 65536 (64 MiB)
	defaultThreads = 4              // p
	saltLen        = 16             // bytes
	keyLen         = 32             // bytes
)

// Params are the argon2id cost parameters plus the derived-key length.
type Params struct {
	Time     uint32 // t — iterations
	MemoryKB uint32 // m — memory in KiB
	Threads  uint8  // p — parallelism
	KeyLen   uint32 // derived key length in bytes
}

// DefaultParams returns the SPEC v2 §16 frozen parameters used for a new hash.
func DefaultParams() Params {
	return Params{Time: defaultTime, MemoryKB: defaultMemory, Threads: defaultThreads, KeyLen: keyLen}
}

// HashFile is the parsed node-side secret (SPEC v2 §16 PHC string).
type HashFile struct {
	Version  int    // argon2 version (19)
	Time     uint32 // t
	MemoryKB uint32 // m (KiB)
	Threads  uint8  // p
	Salt     []byte // raw salt bytes
	Hash     []byte // raw derived key bytes
}

// Derive runs argon2id over password with the given salt and parameters and
// returns the raw derived key. Pure x/crypto — no hand-rolled crypto.
func Derive(password, salt []byte, p Params) []byte {
	return argon2.IDKey(password, salt, p.Time, p.MemoryKB, p.Threads, p.KeyLen)
}

// NewHashFile derives a fresh hash for password using a new random 16-byte salt
// and the frozen default parameters. crypto/rand failure is surfaced, never
// silently substituted with weak randomness.
func NewHashFile(password []byte) (HashFile, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return HashFile{}, fmt.Errorf("auth: generate salt: %w", err)
	}
	p := DefaultParams()
	hash := Derive(password, salt, p)
	return HashFile{
		Version:  argonVersion,
		Time:     p.Time,
		MemoryKB: p.MemoryKB,
		Threads:  p.Threads,
		Salt:     salt,
		Hash:     hash,
	}, nil
}

// Verify reports whether password matches the stored hash. It re-derives using
// the parameters and salt FROM hf (not the package defaults) and compares in
// constant time. A zero-length stored hash never accepts.
func Verify(hf HashFile, password []byte) bool {
	if len(hf.Hash) == 0 {
		return false
	}
	p := Params{Time: hf.Time, MemoryKB: hf.MemoryKB, Threads: hf.Threads, KeyLen: uint32(len(hf.Hash))}
	got := Derive(password, hf.Salt, p)
	return subtle.ConstantTimeCompare(got, hf.Hash) == 1
}

// rawB64 is the standard-alphabet, unpadded base64 used by the PHC string
// (SPEC v2 §16: `=` stripped).
var rawB64 = base64.RawStdEncoding

// FormatPHC encodes hf as the canonical PHC string (SPEC v2 §16):
//
//	$argon2id$v=19$m=65536,t=3,p=4$<salt-b64>$<hash-b64>
//
// Leading `$`, unpadded standard base64 (DG-27.1).
func FormatPHC(hf HashFile) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		hf.Version, hf.MemoryKB, hf.Time, hf.Threads,
		rawB64.EncodeToString(hf.Salt), rawB64.EncodeToString(hf.Hash))
}

// ParsePHC strictly decodes a canonical PHC argon2id string into a HashFile.
// Any deviation (wrong field count, missing leading `$`, non-argon2id algorithm,
// malformed params, bad base64) is an error — never a partial or false-accepting
// parse.
func ParsePHC(s string) (HashFile, error) {
	s = strings.TrimSpace(s)
	// "$argon2id$v=19$m=..,t=..,p=..$salt$hash" splits to 6 parts with a leading "".
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" {
		return HashFile{}, fmt.Errorf("auth: malformed PHC string")
	}
	if parts[1] != "argon2id" {
		return HashFile{}, fmt.Errorf("auth: unsupported algorithm %q (want argon2id)", parts[1])
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return HashFile{}, fmt.Errorf("auth: bad version field %q", parts[2])
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return HashFile{}, fmt.Errorf("auth: bad parameter field %q", parts[3])
	}
	salt, err := rawB64.DecodeString(parts[4])
	if err != nil {
		return HashFile{}, fmt.Errorf("auth: bad salt encoding: %w", err)
	}
	hash, err := rawB64.DecodeString(parts[5])
	if err != nil {
		return HashFile{}, fmt.Errorf("auth: bad hash encoding: %w", err)
	}
	if len(salt) == 0 || len(hash) == 0 {
		return HashFile{}, fmt.Errorf("auth: empty salt or hash")
	}
	return HashFile{Version: version, Time: t, MemoryKB: m, Threads: p, Salt: salt, Hash: hash}, nil
}

// HashFilePath joins the store-relative HashFileRel onto a node base (typically
// <base_path>/<subpath>), returning the on-node path of the password file.
func HashFilePath(base string) string {
	return path.Join(base, HashFileRel)
}

// WriteHashFile writes hf to path as a single-line canonical PHC string with
// mode 0600, using the atomic temp+fsync+rename discipline so a crash never
// leaves a torn or world-readable secret. The parent directory is created 0700.
func WriteHashFile(p string, hf HashFile) error {
	data := []byte(FormatPHC(hf) + "\n")
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("auth: create auth dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".passwd-*")
	if err != nil {
		return fmt.Errorf("auth: temp passwd file: %w", err)
	}
	tmpName := tmp.Name()
	// CreateTemp is already 0600, but be explicit — this is a secret.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("auth: chmod passwd: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("auth: write passwd: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("auth: fsync passwd: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("auth: close passwd: %w", err)
	}
	if err := os.Rename(tmpName, p); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("auth: rename passwd: %w", err)
	}
	// Flush the directory entry so the rename of this secret survives a crash,
	// matching the frozen atomicity standard (catalog/wal do the same).
	fsyncDir(dir)
	return nil
}

// fsyncDir flushes a directory entry so a rename into it survives a crash. A
// platform that cannot open or sync a directory is tolerated (best effort) — the
// rename already landed.
func fsyncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}

// LoadHashFile reads and parses the PHC hash file at path. ok is false (with a
// nil error) when the file does not exist — meaning NO password is set on this
// node, which callers must treat as "mutations refused", never as "open".
func LoadHashFile(p string) (hf HashFile, ok bool, err error) {
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return HashFile{}, false, nil
		}
		return HashFile{}, false, fmt.Errorf("auth: read passwd file: %w", err)
	}
	hf, err = ParsePHC(string(data))
	if err != nil {
		return HashFile{}, false, err
	}
	return hf, true, nil
}
