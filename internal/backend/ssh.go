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

// shellQuote single-quotes s for safe interpolation into a remote POSIX shell
// command, escaping embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
