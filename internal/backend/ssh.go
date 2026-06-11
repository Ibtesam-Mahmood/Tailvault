package backend

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// Runner indirects command execution so the SSH backend is unit-testable
// without a real ssh / node. The default execRunner shells to `ssh`. in feeds
// remote stdin (may be nil); remote stdout streams to out (may be nil);
// captured stderr is returned alongside the error.
type Runner interface {
	Run(ctx context.Context, in io.Reader, out io.Writer, name string, args ...string) (stderr []byte, err error)
}

// SSH streams blobs over `ssh user@node` using POSIX remote helpers
// (cat/dd/stat/mkdir/mv/rm/find). Storage is content-addressed under BasePath.
type SSH struct {
	User     string
	Node     string // MagicDNS name or 100.x IP
	BasePath string // <base_path>/<subpath>

	// Ping preflights node liveness before any byte moves; injected from
	// internal/tailscale. A failure becomes TV-NODE-01 before any transfer, so
	// a down node never leaves a partial upload.
	Ping func(ctx context.Context, node string) error

	// R is the exec seam; nil means the default ssh runner.
	R Runner
}

const missingMarker = "__TV_MISSING__"

func (s *SSH) runner() Runner {
	if s.R != nil {
		return s.R
	}
	return execRunner{}
}

// target is the ssh destination, "user@node" or just "node".
func (s *SSH) target() string {
	if s.User != "" {
		return s.User + "@" + s.Node
	}
	return s.Node
}

// remotePath joins a store-relative key onto BasePath.
func (s *SSH) remotePath(key string) string {
	return path.Join(s.BasePath, key)
}

// preflight runs the injected Ping; a failure maps to TV-NODE-01 before any
// data moves.
func (s *SSH) preflight(ctx context.Context) error {
	if s.Ping == nil {
		return nil
	}
	if err := s.Ping(ctx, s.Node); err != nil {
		return tserr.NodeOfflineErr(s.Node, err)
	}
	return nil
}

// ssh runs a remote shell command, streaming in->stdin and stdout->out.
func (s *SSH) ssh(ctx context.Context, in io.Reader, out io.Writer, remoteCmd string) ([]byte, error) {
	return s.runner().Run(ctx, in, out, "ssh", s.target(), remoteCmd)
}

func (s *SSH) Stat(ctx context.Context, key string) (Meta, error) {
	if err := s.preflight(ctx); err != nil {
		return Meta{}, err
	}
	p := shellQuote(s.remotePath(key))
	cmd := fmt.Sprintf("if [ -f %s ]; then wc -c < %s; else echo %s; fi", p, p, missingMarker)
	var buf bytes.Buffer
	if _, err := s.ssh(ctx, nil, &buf, cmd); err != nil {
		return Meta{}, err
	}
	out := strings.TrimSpace(buf.String())
	if out == missingMarker || out == "" {
		return Meta{Exists: false}, nil
	}
	size, err := strconv.ParseInt(strings.Fields(out)[0], 10, 64)
	if err != nil {
		return Meta{}, fmt.Errorf("backend/ssh: parse size %q: %w", out, err)
	}
	return Meta{Exists: true, Size: size}, nil
}

func (s *SSH) Get(ctx context.Context, key string, w io.Writer) error {
	if err := s.preflight(ctx); err != nil {
		return err
	}
	p := shellQuote(s.remotePath(key))
	// Distinguish "missing" from other failures with an explicit test -f.
	cmd := fmt.Sprintf("if [ -f %s ]; then cat %s; else echo %s >&2; exit 7; fi", p, p, missingMarker)
	stderr, err := s.ssh(ctx, nil, w, cmd)
	if err != nil {
		if strings.Contains(string(stderr), missingMarker) {
			return objMissing(key)
		}
		return classifyWrite(s.Node, stderr, err)
	}
	return nil
}

// Exec runs an arbitrary command ON the node over the SSH channel, piping in to
// its stdin and capturing stderr. It is the seam for node-side helpers — e.g.
// `tailvault node verify-passwd`, whose exit status authorizes a mutating op —
// distinct from the object operations. A nil error means the remote command
// exited 0; a non-nil error carries the captured stderr so the caller can
// classify the remote exit (e.g. a TV-AUTH-01 rejection vs an ssh-level
// failure). preflight maps an unreachable node to TV-NODE-01 before anything
// runs. The bytes written to in never touch local disk and only the exit status
// (not stdout) is relied upon, so a node-side secret check leaks nothing back.
func (s *SSH) Exec(ctx context.Context, in io.Reader, remoteCmd string) (stderr []byte, err error) {
	if perr := s.preflight(ctx); perr != nil {
		return nil, perr
	}
	return s.ssh(ctx, in, nil, remoteCmd)
}

// HashObject runs `sha256sum` on the node and returns only the 64-hex digest —
// the blob bytes never cross the tailnet (the DEV-C1 / GH-2 short-circuit). It
// mirrors Get's missing-vs-failure classification: an explicit `[ -f ]` test
// distinguishes a missing blob (TV-OBJ-01) from a node/permission failure
// (TV-NODE-01/02). Output is parsed strictly — exactly 64 lowercase hex before
// the first space, never a silent success on a misconfigured node.
func (s *SSH) HashObject(ctx context.Context, key string) (string, error) {
	if err := s.preflight(ctx); err != nil {
		return "", err
	}
	p := shellQuote(s.remotePath(key))
	// No `--` before the path: it is always an absolute, shell-quoted path, and
	// busybox `sha256sum` does not accept the `--` end-of-options marker that
	// coreutils does — dropping it keeps both helper families working.
	cmd := fmt.Sprintf("if [ -f %s ]; then sha256sum %s; else echo %s >&2; exit 7; fi", p, p, missingMarker)
	var buf bytes.Buffer
	stderr, err := s.ssh(ctx, nil, &buf, cmd)
	if err != nil {
		if strings.Contains(string(stderr), missingMarker) {
			return "", objMissing(key)
		}
		return "", classifyWrite(s.Node, stderr, err)
	}
	return parseSha256Sum(buf.String())
}

func (s *SSH) Put(ctx context.Context, key string, r io.Reader) error {
	if err := s.preflight(ctx); err != nil {
		return err
	}
	// Content-addressed dedup: skip the transfer entirely if it already exists.
	m, err := s.Stat(ctx, key)
	if err != nil {
		return err
	}
	if m.Exists {
		return nil
	}

	full := s.remotePath(key)
	dir := shellQuote(path.Dir(full))
	dst := shellQuote(full)
	tmp := shellQuote(full + ".tmp")
	// mkdir parent; stream stdin to a tmp then atomically mv into place.
	cmd := fmt.Sprintf("mkdir -p %s && cat > %s && mv %s %s", dir, tmp, tmp, dst)
	stderr, err := s.ssh(ctx, r, nil, cmd)
	if err != nil {
		return classifyWrite(s.Node, stderr, err)
	}
	return nil
}

// PutOverwrite atomically replaces a mutable key: stream stdin to a temp file
// then `mv` it over the target (POSIX rename is an atomic overwrite on one
// filesystem). Unlike Put it does NOT dedup on Stat, so an in-place update of a
// mutable key (e.g. meta/catalog.toml) always lands.
func (s *SSH) PutOverwrite(ctx context.Context, key string, r io.Reader) error {
	if err := s.preflight(ctx); err != nil {
		return err
	}
	full := s.remotePath(key)
	dir := shellQuote(path.Dir(full))
	dst := shellQuote(full)
	tmp := shellQuote(full + ".tmp")
	cmd := fmt.Sprintf("mkdir -p %s && cat > %s && mv %s %s", dir, tmp, tmp, dst)
	stderr, err := s.ssh(ctx, r, nil, cmd)
	if err != nil {
		return classifyWrite(s.Node, stderr, err)
	}
	return nil
}

func (s *SSH) Delete(ctx context.Context, key string) error {
	if err := s.preflight(ctx); err != nil {
		return err
	}
	cmd := fmt.Sprintf("rm -f %s", shellQuote(s.remotePath(key)))
	if _, err := s.ssh(ctx, nil, nil, cmd); err != nil {
		return err
	}
	return nil
}

func (s *SSH) List(ctx context.Context, prefix string) ([]string, error) {
	if err := s.preflight(ctx); err != nil {
		return nil, err
	}
	base := shellQuote(s.BasePath)
	// find may exit non-zero if BasePath does not exist yet; treat that as empty.
	cmd := fmt.Sprintf("find %s -type f 2>/dev/null", base)
	var buf bytes.Buffer
	if _, err := s.ssh(ctx, nil, &buf, cmd); err != nil {
		// An empty/absent tree is not an error for List.
		if buf.Len() == 0 {
			return nil, nil
		}
		return nil, err
	}
	var keys []string
	basePrefix := strings.TrimSuffix(s.BasePath, "/") + "/"
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key := strings.TrimPrefix(line, basePrefix)
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// classifyWrite maps a write failure's stderr to TV-NODE-02 (reachable but not
// writable) when it looks like a permission/space/read-only error; otherwise it
// returns a wrapped generic error.
func classifyWrite(node string, stderr []byte, err error) error {
	s := strings.ToLower(string(stderr))
	switch {
	case strings.Contains(s, "permission denied"),
		strings.Contains(s, "read-only file system"),
		strings.Contains(s, "no space left"),
		strings.Contains(s, "operation not permitted"):
		return tserr.NodeNotWritableErr(node, err)
	}
	if len(stderr) > 0 {
		return fmt.Errorf("backend/ssh: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return fmt.Errorf("backend/ssh: %w", err)
}

// parseSha256Sum extracts the digest from `sha256sum` output. The format differs
// subtly across coreutils ("<hex>  <name>") and busybox ("<hex>  <name>" or
// "<hex> *<name>"), so only the leading whitespace-delimited token is trusted,
// and it must be exactly 64 lowercase hex characters; anything else is an error,
// never a silent success.
func parseSha256Sum(out string) (string, error) {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", fmt.Errorf("backend/ssh: empty sha256sum output")
	}
	digest := fields[0]
	if len(digest) != 64 || !isLowerHex(digest) {
		return "", fmt.Errorf("backend/ssh: unexpected sha256sum output %q", strings.TrimSpace(out))
	}
	return digest, nil
}

// isLowerHex reports whether s is non-empty and consists solely of 0-9 / a-f.
func isLowerHex(s string) bool {
	if s == "" {
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

// shellQuote single-quotes s for safe interpolation into a remote POSIX shell
// command, escaping embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellQuote exposes shellQuote so command-layer code building a remote command
// for Exec (e.g. the node-side password verifier) can quote arguments with the
// SAME escaping the backend uses internally.
func ShellQuote(s string) string { return shellQuote(s) }
