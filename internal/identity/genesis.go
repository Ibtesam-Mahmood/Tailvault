// Package identity owns federated file identity (SPEC v2 §11–§12): the genesis
// record, the genesis-hash file ID, and pull receipts. Dual addressing (D19): a
// file has a stable, location-independent ID (used for all linking/lock/
// reference) and a logical path (display/navigation). Moves change the path,
// never the ID; the ID is deliberately NOT the content hash (manual files drift
// between scans — H12).
//
// id = sha256(canonical genesis record). The serialization is byte-exact and
// frozen in SPEC v2 §11: one whitespace byte changes every file ID in
// existence, so this package renders the record explicitly rather than through
// a TOML library (whose output can shift across versions). Plain errors only
// (§8 layering).
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// Genesis is the immutable birth record of a federated file (SPEC v2 §11). The
// field order here is the canonical serialization order — do not reorder.
type Genesis struct {
	ContentSHA256 string `toml:"content_sha256"` // original content hash at ingest
	OriginalPath  string `toml:"original_path"`  // original vault-relative path
	IngestOpID    string `toml:"ingest_op_id"`   // wal op id of the ingest entry
	OriginNode    string `toml:"origin_node"`
}

// CanonicalBytes renders the SPEC v2 §11 frozen serialization: four lines in
// fixed field order, each `key = "value"` with one space around `=`, the value
// double-quoted with TOML basic-string escaping, each line terminated by a
// single LF (including the last), UTF-8, no BOM. Byte-exact determinism is
// normative — the genesis hash depends on these exact bytes.
func (g Genesis) CanonicalBytes() ([]byte, error) {
	var b strings.Builder
	writeField(&b, "content_sha256", g.ContentSHA256)
	writeField(&b, "original_path", g.OriginalPath)
	writeField(&b, "ingest_op_id", g.IngestOpID)
	writeField(&b, "origin_node", g.OriginNode)
	return []byte(b.String()), nil
}

func writeField(b *strings.Builder, key, val string) {
	b.WriteString(key)
	b.WriteString(` = "`)
	b.WriteString(escapeBasic(val))
	b.WriteString("\"\n")
}

// escapeBasic applies TOML basic-string escaping: backslash and quote, the named
// control escapes, and any other control char as \uXXXX.
func escapeBasic(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// MintID computes id = lowercasehex(sha256(CanonicalBytes(g))).
func MintID(g Genesis) (string, error) {
	cb, err := g.CanonicalBytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cb)
	return hex.EncodeToString(sum[:]), nil
}

// Verify reports whether g self-certifies the claimed id (MintID(g) == id,
// case-insensitive hex compare).
func Verify(g Genesis, id string) (bool, error) {
	got, err := MintID(g)
	if err != nil {
		return false, err
	}
	return got == strings.ToLower(strings.TrimSpace(id)), nil
}

// Short returns the 12-hex display form (like a git short SHA). Display only;
// all storage/lookup uses the full 64-hex id.
func Short(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// FromIngestEntry extracts the genesis record from an op_type="ingest" WAL entry.
// The content hash and original path come from the entry args; the ingest op id
// is the entry's OpID; the origin node is supplied by the caller (the node whose
// WAL holds the entry). It errors on any non-ingest op type.
func FromIngestEntry(e wal.Entry, originNode string) (Genesis, error) {
	if e.OpType != wal.OpIngest {
		return Genesis{}, fmt.Errorf("identity: not an ingest entry (op_type=%q)", e.OpType)
	}
	g := Genesis{
		ContentSHA256: e.Args["content_sha256"],
		OriginalPath:  e.Args["path"],
		IngestOpID:    e.OpID,
		OriginNode:    originNode,
	}
	if err := g.Validate(); err != nil {
		return Genesis{}, err
	}
	return g, nil
}

// Validate checks the record's basic invariants (64-hex content sha, non-empty
// fields).
func (g Genesis) Validate() error {
	if !isHex64(g.ContentSHA256) {
		return errors.New("identity: content_sha256 is not 64 hex chars")
	}
	if g.OriginalPath == "" {
		return errors.New("identity: original_path is empty")
	}
	if g.IngestOpID == "" {
		return errors.New("identity: ingest_op_id is empty")
	}
	if g.OriginNode == "" {
		return errors.New("identity: origin_node is empty")
	}
	return nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
