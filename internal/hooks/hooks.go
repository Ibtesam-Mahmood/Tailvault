// Package hooks installs the git hooks that bind tailvault to git push/pull.
//
// Three hooks are installed into the repository's hooks directory:
//
//   - pre-push      runs "tailvault push" and propagates its exit code, so a
//     failed push (node down, missing blob) aborts the git push before refs
//     advance and surfaces the same tserr exit bucket.
//   - post-merge    runs "tailvault pull" to fetch blobs the merged tree needs.
//   - post-checkout runs "tailvault pull" to fetch blobs for the checked-out
//     tree (eager resolution, per SPEC Q6).
//
// Hook scripts invoke tailvault by the absolute path resolved at install time
// (Task 18 passes os.Executable()), so they work regardless of the user's PATH
// when git runs them. The hooks themselves do no preflight — they just call
// tailvault, which preflights and returns the right bucketed code.
package hooks

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// sentinel marks a hook script as tailvault-managed. Its presence lets a
// re-install distinguish our own hooks (safe to overwrite silently) from a
// pre-existing foreign hook (warned about before overwriting).
const sentinel = "# tailvault-managed hook"

// hookNames are the three hooks tailvault installs, in install order.
var hookNames = []string{"pre-push", "post-merge", "post-checkout"}

// InstallHooks writes pre-push, post-merge and post-checkout into the repo's
// hooks dir (honouring core.hooksPath via "git rev-parse --git-path hooks"),
// each executable (0o755) and each invoking the absolute tailvault binPath.
//
// It is idempotent: re-running rewrites the scripts with the current binPath
// (an upgrade may move the binary). A pre-existing non-tailvault hook is
// overwritten with a warning to stderr.
func InstallHooks(repoRoot, binPath string) error {
	return installHooks(repoRoot, binPath, os.Stderr)
}

// installHooks is the testable core; warn receives overwrite warnings.
func installHooks(repoRoot, binPath string, warn io.Writer) error {
	if binPath == "" {
		return errors.New("hooks: empty binary path")
	}
	dir, err := hooksDir(repoRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("hooks: create hooks dir %s: %w", dir, err)
	}

	bodies := map[string]string{
		"pre-push":      prePushBody(binPath),
		"post-merge":    pullHookBody(binPath),
		"post-checkout": pullHookBody(binPath),
	}
	for _, name := range hookNames {
		p := filepath.Join(dir, name)
		if foreign, err := isForeignHook(p); err != nil {
			return err
		} else if foreign && warn != nil {
			fmt.Fprintf(warn, "tailvault: overwriting existing non-tailvault %s hook at %s\n", name, p)
		}
		if err := os.WriteFile(p, []byte(bodies[name]), 0o755); err != nil {
			return fmt.Errorf("hooks: write %s: %w", name, err)
		}
		// os.WriteFile only applies the mode on create; force the exec bit in
		// case the file pre-existed with a non-executable mode.
		if err := os.Chmod(p, 0o755); err != nil {
			return fmt.Errorf("hooks: chmod %s: %w", name, err)
		}
	}
	return nil
}

// prePushBody renders the pre-push hook. exec replaces the shell with tailvault
// so its exit status *is* the hook's status → git aborts the push on non-zero.
func prePushBody(binPath string) string {
	return "#!/bin/sh\n" + sentinel + "\nexec \"" + binPath + "\" push\n"
}

// pullHookBody renders post-merge / post-checkout. "|| exit $?" forwards a
// non-zero pull failure. post-checkout is run eagerly on any checkout (Q6);
// git passes it $1 $2 $3 (with $3=1 for a branch checkout) which v1 ignores.
func pullHookBody(binPath string) string {
	return "#!/bin/sh\n" + sentinel + "\n\"" + binPath + "\" pull || exit $?\n"
}

// hooksDir resolves the repo's hooks directory, honouring core.hooksPath and
// worktrees via "git rev-parse --git-path hooks". A relative result is joined
// onto repoRoot.
func hooksDir(repoRoot string) (string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--git-path", "hooks").Output()
	if err != nil {
		return "", fmt.Errorf("hooks: resolve hooks dir (is %s a git repo?): %w", repoRoot, err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", fmt.Errorf("hooks: empty hooks dir for repo %s", repoRoot)
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repoRoot, dir)
	}
	return dir, nil
}

// isForeignHook reports whether a hook file exists and is NOT tailvault-managed
// (i.e. lacks the sentinel). A missing file is not foreign.
func isForeignHook(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("hooks: read %s: %w", path, err)
	}
	return !strings.Contains(string(b), sentinel), nil
}
