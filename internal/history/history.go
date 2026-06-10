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
	"crypto/sha256"
	"encoding/hex"
	"path"
	"path/filepath"
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
