// Package gitglue wraps the handful of git plumbing commands tailvault's
// command layer needs: locating the repo, reading/writing git config, reading a
// file at a branch tip (for the GC keep-set), enumerating local branches, and
// staging a file. Each is a thin, testable shell-out to the git binary; the
// engine packages stay git-free and these wrappers live only behind commands.
package gitglue

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// RepoRoot returns the absolute working-tree root for the repo containing dir
// (use "" for the current directory). The error is suitable for a
// config/precondition (exit 2) wrap when dir is not inside a git repo.
func RepoRoot(dir string) (string, error) {
	out, err := run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("gitglue: not a git repository (%s): %w", dirLabel(dir), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ConfigSet sets a repo-local git config key (overwriting; inherently idempotent
// for a deterministic value).
func ConfigSet(repoRoot, key, value string) error {
	if _, err := run(repoRoot, "config", key, value); err != nil {
		return fmt.Errorf("gitglue: git config %s: %w", key, err)
	}
	return nil
}

// ConfigGet returns the value of a repo-local git config key, trimmed. A missing
// key returns ("", nil) so callers can treat absence as empty.
func ConfigGet(repoRoot, key string) (string, error) {
	out, err := run(repoRoot, "config", "--get", key)
	if err != nil {
		// `git config --get` exits 1 when the key is absent; treat as empty.
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("gitglue: git config --get %s: %w", key, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// LocalBranches returns the short names of every local branch (refs/heads/*).
func LocalBranches(repoRoot string) ([]string, error) {
	out, err := run(repoRoot, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, fmt.Errorf("gitglue: enumerate branches: %w", err)
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if b := strings.TrimSpace(line); b != "" {
			branches = append(branches, b)
		}
	}
	return branches, nil
}

// ReadFileAtRef returns the bytes of path as committed at ref (e.g. a branch
// name). found is false (with a nil error) when the path does not exist at that
// ref — GC uses this to skip a branch that has no committed tailvault.lock.
func ReadFileAtRef(repoRoot, ref, path string) (content []byte, found bool, err error) {
	spec := ref + ":" + path
	// cat-file -e exits non-zero iff the object does not exist at that ref; it is
	// purely an existence test, so any non-zero exit means "not found".
	if _, e := run(repoRoot, "cat-file", "-e", spec); e != nil {
		var ee *exec.ExitError
		if errors.As(e, &ee) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("gitglue: probe %s: %w", spec, e)
	}
	out, e := run(repoRoot, "show", spec)
	if e != nil {
		return nil, false, fmt.Errorf("gitglue: show %s: %w", spec, e)
	}
	return out, true, nil
}

// AddPath stages path (git add) without committing.
func AddPath(repoRoot, path string) error {
	if _, err := run(repoRoot, "add", path); err != nil {
		return fmt.Errorf("gitglue: git add %s: %w", path, err)
	}
	return nil
}

// run executes a git subcommand in repoRoot (or the current dir if empty) and
// returns stdout. Stderr is folded into the error for legible failures.
func run(repoRoot string, args ...string) ([]byte, error) {
	full := args
	if repoRoot != "" {
		full = append([]string{"-C", repoRoot}, args...)
	}
	cmd := exec.Command("git", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

func dirLabel(dir string) string {
	if dir == "" {
		return "current directory"
	}
	return dir
}
