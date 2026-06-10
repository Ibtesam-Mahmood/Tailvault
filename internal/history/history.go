// Package history implements opt-in version history for vault-managed files.
//
// When a file resolves to history = true, a content change during push appends
// the new sha (newest-first) both to the node-side ref list refs/<path-id> and
// to the lock entry's versions[]. Because those superseded shas live in
// versions[], the GC keep-set (Task 16) unions them and they are exempt from
// auto_delete / GC. History-off files have no versions[] and no refs/ entry.
//
// PathID is the stable, content-independent key for a logical path's ref list;
// AppendVersion maintains refs/<path-id> on the backend.
package history

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"path/filepath"
	"strings"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
)

// refPrefix is the storage prefix for per-path version ref lists.
const refPrefix = "refs/"

// PathID returns a stable, filesystem-safe id for a file's logical path: the
// hex sha256 of the slash-normalized, cleaned repo-relative path.
//
// It is content-INDEPENDENT by design (per the glossary): the ref key must not
// change as the file's content changes, or history would break. Equivalent
// spellings of the same path ("a/b.pdf", "./a/b.pdf", "a//b.pdf") normalize to
// the same id; different paths produce different ids.
func PathID(logicalPath string) string {
	sum := sha256.Sum256([]byte(normalizePath(logicalPath)))
	return hex.EncodeToString(sum[:])
}

// RefKey is the backend key for a path's version ref list: refs/<path-id>.
func RefKey(logicalPath string) string {
	return refPrefix + PathID(logicalPath)
}

// normalizePath canonicalizes a repo-relative logical path: OS separators are
// converted to slashes and the path is lexically cleaned (collapsing "."
// segments and duplicate slashes). It is purely lexical — no filesystem access.
func normalizePath(p string) string {
	return path.Clean(filepath.ToSlash(p))
}

// AppendVersion prepends sha (newest-first) to refs/<path-id> on the backend and
// returns the full list. It reads the existing newline-delimited list, and if
// sha already heads it (a re-push of identical content) returns it unchanged.
// The same newest-first ordering is the contract revert (Task 21) and the GC
// keep-set depend on, so versions[] in the lock must be kept in lockstep.
//
// refs/<path-id> is a MUTABLE list, unlike content-addressed objects/<sha>;
// Backend.Put dedups on Stat (a no-op when the key exists), so the ref is
// rewritten via Delete-then-Put to force the overwrite.
func AppendVersion(ctx context.Context, b backend.Backend, pathID, sha string) ([]string, error) {
	key := refPrefix + pathID
	existing, err := readRef(ctx, b, key)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 && existing[0] == sha {
		return existing, nil // re-push of the same content: no-op
	}
	updated := append([]string{sha}, existing...)
	if err := writeRef(ctx, b, key, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

// ReadVersions returns the newest-first sha list recorded at refs/<path-id>, or
// nil if the ref does not exist yet.
func ReadVersions(ctx context.Context, b backend.Backend, pathID string) ([]string, error) {
	return readRef(ctx, b, refPrefix+pathID)
}

// readRef fetches and parses a ref list; a missing ref is an empty list, not an
// error.
func readRef(ctx context.Context, b backend.Backend, key string) ([]string, error) {
	var buf bytes.Buffer
	if err := b.Get(ctx, key, &buf); err != nil {
		if errors.Is(err, backend.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return splitLines(buf.Bytes()), nil
}

// writeRef overwrites refs/<path-id> with the newline-delimited list. Delete
// first because content-addressed Put dedups on an existing key.
func writeRef(ctx context.Context, b backend.Backend, key string, list []string) error {
	if err := b.Delete(ctx, key); err != nil {
		return err
	}
	data := strings.Join(list, "\n") + "\n"
	return b.Put(ctx, key, strings.NewReader(data))
}

// splitLines splits a newline-delimited ref body into non-empty shas.
func splitLines(data []byte) []string {
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}
