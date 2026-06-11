// Package wal owns the per-node write-ahead log (SPEC v2 §10) — the concurrency
// and partial-failure model for the whole federation. Every mutating op appends
// an immutable intent entry before touching anything, executes, then records a
// terminal sibling marker. Entries are hash-chained (each embeds the sha256 of
// the previous entry's canonical bytes) so any tampering is detectable on read
// (tamper-evident, no consensus — D17).
//
// WAL-as-lock (D12): a blob has exactly one home, so every op on it touches that
// home's WAL — appending the intent IS acquiring the per-blob lock. Blocking is
// per-blob ordering only; ops on different blobs are independent.
//
// Serverless: no goroutine, no watcher, no daemon — every function is a plain
// client-driven call over a backend.Backend. Per the SPEC §8 layering rule this
// is a leaf package returning plain errors (with typed sentinels); the command
// boundary maps them to tserr (a broken chain → TV-FED-03, exit 6).
package wal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// State is the effective lifecycle state of an op, derived from sibling marker
// files (the entry itself is immutable and carries no state field — DG-27.2).
type State string

const (
	StateIntent State = "intent"
	StateDone   State = "done"
	StateFailed State = "failed"
)

// Op types (SPEC v2 §10).
const (
	OpIngest   = "ingest"
	OpMove     = "move"
	OpDelete   = "delete"
	OpSyncMode = "sync_mode"
	OpGC       = "gc"
	OpRoster   = "roster"
	OpScan     = "scan"
	OpPasswd   = "passwd"  // password rotation (task-46); BlobRefs=["meta/auth/passwd"]
	OpRestore  = "restore" // identity restoration (task-48); BlobRefs=[restored file id]; args carry the genesis preimage (fix-35-B, projection-sufficient)
)

// ZeroHash is the genesis prev_hash: 64 hex zeros (SPEC v2 §10).
const ZeroHash = "0000000000000000000000000000000000000000000000000000000000000000"

// Entry mirrors one immutable meta/wal/<seq>-<op_id>.toml file (SPEC v2 §10).
// Its declaration order IS the canonical on-disk field order. It carries no
// state/updated_at: the persisted entry is immutable so the hash chain never
// re-hashes; effective state lives in sibling .done/.failed markers (DG-27.2).
type Entry struct {
	Seq       uint64            `toml:"seq"`
	OpID      string            `toml:"op_id"`     // UUIDv4, lowercase hex, no dashes
	PrevHash  string            `toml:"prev_hash"` // 64-hex; ZeroHash for genesis
	OpType    string            `toml:"op_type"`   // ingest|move|delete|sync_mode|gc|roster|scan
	BlobRefs  []string          `toml:"blob_refs"` // file IDs this op locks (WAL-as-lock)
	Actor     string            `toml:"actor"`     // whois identity, else git user.email
	CreatedAt time.Time         `toml:"created_at"`
	Args      map[string]string `toml:"args"` // op-typed args (table; emitted last)
}

// NewOpID returns a random UUIDv4 rendered as 32 lowercase hex chars (no
// dashes), via crypto/rand.
func NewOpID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is not recoverable for a unique-id mint.
		panic("wal: crypto/rand failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return hex.EncodeToString(b[:])
}

// Encode renders an entry's canonical bytes by EXPLICIT byte construction — NOT
// via a TOML marshaler. This is deliberate and load-bearing: these bytes feed the
// hash chain (and fed_id), so the serialization must be byte-stable forever and
// independent of any library's rendering quirks (a go-toml version bump must never
// silently change an on-disk chain's hashes → TV-FED-03). The output is still
// valid TOML (Decode reads it back). Frozen format (fixed field order, LF
// endings, double-quoted basic strings with TOML escaping, bare RFC3339Nano
// datetime, sorted args keys, args table last and omitted when empty):
//
//	seq = <int>
//	op_id = "<hex>"
//	prev_hash = "<hex>"
//	op_type = "<type>"
//	blob_refs = ["<id>", ...]
//	actor = "<actor>"
//	created_at = <RFC3339Nano UTC>
//	[args]            # only when non-empty
//	<sorted key> = "<value>"
func Encode(e Entry) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "seq = %d\n", e.Seq)
	writeKV(&b, "op_id", e.OpID)
	writeKV(&b, "prev_hash", e.PrevHash)
	writeKV(&b, "op_type", e.OpType)
	b.WriteString("blob_refs = [")
	for i, r := range e.BlobRefs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('"')
		b.WriteString(escapeTOML(r))
		b.WriteString(`"`)
	}
	b.WriteString("]\n")
	writeKV(&b, "actor", e.Actor)
	fmt.Fprintf(&b, "created_at = %s\n", e.CreatedAt.UTC().Format(time.RFC3339Nano))
	if len(e.Args) > 0 {
		keys := make([]string, 0, len(e.Args))
		for k := range e.Args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("\n[args]\n")
		for _, k := range keys {
			writeKV(&b, k, e.Args[k])
		}
	}
	return []byte(b.String()), nil
}

func writeKV(b *strings.Builder, key, val string) {
	b.WriteString(key)
	b.WriteString(` = "`)
	b.WriteString(escapeTOML(val))
	b.WriteString("\"\n")
}

// escapeTOML applies TOML basic-string escaping (backslash, quote, the named
// control escapes, other control chars as \uXXXX).
func escapeTOML(s string) string {
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

// Decode parses an entry's canonical bytes (valid TOML) back into an Entry.
func Decode(b []byte) (Entry, error) {
	var e Entry
	if err := toml.Unmarshal(b, &e); err != nil {
		return Entry{}, fmt.Errorf("wal: decode: %w", err)
	}
	return e, nil
}

// Hash returns the sha256 hex of Encode(e) — the entry's link value in the
// chain. For honest (untampered) entries this equals the sha256 of the on-disk
// file bytes, since the file is exactly Encode(e).
func Hash(e Entry) (string, error) {
	b, err := Encode(e)
	if err != nil {
		return "", err
	}
	return hashBytes(b), nil
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
