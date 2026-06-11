// Package pointer owns the serialization of the in-git pointer file: the tiny
// text stand-in git stores in place of a vault-managed file's real bytes. It is
// pure (no I/O beyond byte slices, no Tailscale) so the clean/smudge filter,
// status, push, and pull can all trust one canonical representation.
package pointer

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
)

// Magic is the required first line of every pointer file.
const Magic = "tailvault.v1"

// Pointer is the in-git stand-in for a vault-managed file's bytes.
type Pointer struct {
	SHA256   string // lowercase hex sha256 of the real content
	Size     int64  // size in bytes of the real content
	Location string // location name resolved via locations.toml
}

// Encode renders p as the canonical 4-line pointer file, each line
// newline-terminated:
//
//	tailvault.v1
//	sha256 <hex>
//	size <bytes>
//	location <name>
func Encode(p Pointer) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s\n", Magic)
	fmt.Fprintf(&b, "sha256 %s\n", p.SHA256)
	fmt.Fprintf(&b, "size %d\n", p.Size)
	fmt.Fprintf(&b, "location %s\n", p.Location)
	return b.Bytes()
}

// IsPointer is a cheap, allocation-light, panic-free prefix check used by the
// filter to pass non-pointer content (real PDFs/STLs/etc.) through untouched
// without a full parse. It is true for an encoded pointer and for bare-magic
// input.
func IsPointer(data []byte) bool {
	return bytes.HasPrefix(data, []byte(Magic+"\n")) ||
		bytes.Equal(bytes.TrimRight(data, "\n"), []byte(Magic))
}

// isLowerHex reports whether s is non-empty and all lowercase hex digits.
func isLowerHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// Decode parses a pointer file, rejecting a wrong header, a missing/extra/
// reordered key, a malformed line, or an invalid value. On any error it returns
// a zero-value Pointer and a descriptive error with line context.
func Decode(data []byte) (Pointer, error) {
	var p Pointer
	sc := bufio.NewScanner(bytes.NewReader(data))
	// Allow large-ish lines defensively, though pointers are tiny.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lines := make([]string, 0, 5)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return Pointer{}, fmt.Errorf("pointer: read: %w", err)
	}
	if len(lines) != 4 {
		return Pointer{}, fmt.Errorf("pointer: want 4 lines, got %d", len(lines))
	}
	if lines[0] != Magic {
		return Pointer{}, fmt.Errorf("pointer: line 1: want %q, got %q", Magic, lines[0])
	}

	sha, err := field(lines[1], 2, "sha256")
	if err != nil {
		return Pointer{}, err
	}
	if !isLowerHex(sha) {
		return Pointer{}, fmt.Errorf("pointer: line 2: sha256 must be lowercase hex, got %q", sha)
	}

	sizeStr, err := field(lines[2], 3, "size")
	if err != nil {
		return Pointer{}, err
	}
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return Pointer{}, fmt.Errorf("pointer: line 3: invalid size %q: %w", sizeStr, err)
	}
	if size < 0 {
		return Pointer{}, fmt.Errorf("pointer: line 3: negative size %d", size)
	}

	loc, err := field(lines[3], 4, "location")
	if err != nil {
		return Pointer{}, err
	}
	if loc == "" {
		return Pointer{}, fmt.Errorf("pointer: line 4: location must not be empty")
	}

	p.SHA256 = sha
	p.Size = size
	p.Location = loc
	return p, nil
}

// field parses a "key value" line, requiring the exact key and a non-empty
// value with exactly one separating space.
func field(line string, lineNo int, key string) (string, error) {
	prefix := key + " "
	if len(line) <= len(prefix) || line[:len(prefix)] != prefix {
		return "", fmt.Errorf("pointer: line %d: want %q, got %q", lineNo, key+" <value>", line)
	}
	return line[len(prefix):], nil
}
