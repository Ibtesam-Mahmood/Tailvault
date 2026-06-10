//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/gc"
	"github.com/Ibtesam-Mahmood/tailvault/internal/gitglue"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/pull"
	"github.com/Ibtesam-Mahmood/tailvault/internal/push"
)

// env is a hermetic end-to-end fixture: a temp git working tree + a temp "node"
// directory served by the taildrive backend (real os file ops, content-
// addressed). No tailscale and no real network are involved; the node is a dir.
type env struct {
	t    *testing.T
	root string          // working tree (a git repo)
	node string          // the storage node (taildrive root)
	be   backend.Backend // backend bound to node
	cfg  *config.Config
	ctx  context.Context
}

// newEnv builds the fixture. history toggles the global history rule.
func newEnv(t *testing.T, history bool) *env {
	t.Helper()
	root := t.TempDir()
	node := t.TempDir()
	git(t, root, "init")
	git(t, root, "config", "user.email", "tester@example.com")
	git(t, root, "config", "user.name", "tester")
	cfg := &config.Config{
		Version: 1,
		Storage: config.Storage{Location: "home-pi"},
		Rules: config.Rules{
			MinSize:    "5MB",
			Include:    []string{"**/*.bin"},
			History:    history,
			AutoDelete: true,
		},
	}
	return &env{t: t, root: root, node: node, be: backend.NewTaildrive(node), cfg: cfg, ctx: context.Background()}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (e *env) write(name, content string) {
	e.t.Helper()
	p := filepath.Join(e.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) remove(name string) {
	e.t.Helper()
	if err := os.Remove(filepath.Join(e.root, filepath.FromSlash(name))); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) read(name string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.root, filepath.FromSlash(name)))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

// loadLock reads tailvault.lock, treating a missing file as an empty lock.
func (e *env) loadLock() *lock.Lock {
	e.t.Helper()
	p := filepath.Join(e.root, "tailvault.lock")
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return &lock.Lock{Version: 1}
	}
	lk, err := lock.Load(p)
	if err != nil {
		e.t.Fatalf("load lock: %v", err)
	}
	return lk
}

// okPreflight / downPreflight model a reachable / unreachable node.
func okPreflight(context.Context) error { return nil }

// pushDeps returns push.Deps with a fixed clock and identity.
func (e *env) pushDeps(preflight func(context.Context) error) push.Deps {
	return push.Deps{
		Backend:     e.be,
		Preflight:   preflight,
		Whois:       func(context.Context) (string, error) { return "tester@laptop", nil },
		GitIdentity: func() string { return "tester@example.com" },
		Now:         func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
}

// push runs a push with the given preflight and returns the result; on success
// the lock is written to disk.
func (e *env) push(preflight func(context.Context) error, opts push.Options) (push.Result, error) {
	return push.Run(e.ctx, e.root, e.cfg, e.loadLock(), e.pushDeps(preflight), opts)
}

// pull fetches missing blobs into the working tree.
func (e *env) pull() (pull.Result, error) {
	return pull.Run(e.ctx, e.root, e.loadLock(), pull.Deps{Backend: e.be, Preflight: okPreflight})
}

// gcSweep builds the keep-set across all local branches and sweeps (or dry-runs).
func (e *env) gcSweep(dryRun bool) gc.Plan {
	e.t.Helper()
	branches, err := gitglue.LocalBranches(e.root)
	if err != nil {
		e.t.Fatalf("branches: %v", err)
	}
	locks := map[string]*lock.Lock{}
	for _, br := range branches {
		data, found, err := gitglue.ReadFileAtRef(e.root, br, "tailvault.lock")
		if err != nil {
			e.t.Fatalf("read %s lock: %v", br, err)
		}
		if !found {
			continue
		}
		l, err := lock.Parse(data)
		if err != nil {
			e.t.Fatalf("parse %s lock: %v", br, err)
		}
		locks[br] = l
	}
	keep := gc.BuildKeepSet(locks)
	preserve := gc.BuildPreserveSet(locks)
	stored, err := e.be.List(e.ctx, "objects/")
	if err != nil {
		e.t.Fatalf("list: %v", err)
	}
	plan := gc.PlanSweep(stored, keep, preserve)
	if _, err := gc.Sweep(e.ctx, e.be, plan, dryRun); err != nil {
		e.t.Fatalf("sweep: %v", err)
	}
	return plan
}

// nodeHas reports whether objects/<sha> exists on the node.
func (e *env) nodeHas(sha string) bool {
	m, err := e.be.Stat(e.ctx, "objects/"+sha)
	if err != nil {
		e.t.Fatalf("stat: %v", err)
	}
	return m.Exists
}

// nodeObjectCount returns how many blobs are under objects/ on the node.
func (e *env) nodeObjectCount() int {
	keys, err := e.be.List(e.ctx, "objects/")
	if err != nil {
		e.t.Fatalf("list: %v", err)
	}
	return len(keys)
}

// commitLock stages + commits the current tailvault.lock on the current branch.
func (e *env) commitLock(msg string) {
	e.t.Helper()
	git(e.t, e.root, "add", "-A")
	git(e.t, e.root, "commit", "-m", msg)
}

func shaOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
