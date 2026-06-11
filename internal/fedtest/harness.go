// Package fedtest is the multi-node federation TEST HARNESS (task-39): it spins
// up N simulated federation members — each a real backend.FSBackend over a temp
// dir carrying its own objects/, meta/catalog.toml and meta/wal/ — wired into a
// shared roster, with first-class down-member simulation. Seeding goes through
// the REAL ingest/WAL code paths (internal/ingest), never hand-written fixtures,
// so the harness can never drift from production writers.
//
// It is an exported, documented package (not an _test.go file) so scenario suites
// across packages — and Blocks 4–5 — consume it directly. Nothing here touches
// Tailscale, SSH, or the network: down-members fail through the SAME production
// seams a dead node fails through (the prober errors; backend calls error with a
// TV-NODE-shaped error before any data moves), so error-classification paths are
// what get exercised, not test-only branches.
package fedtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/auth"
	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/ingest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// baseTime is the fixed instant the harness stamps on roster joins and seeded
// ingests, so seeded catalogs/WALs are byte-deterministic across runs (the
// byte-identical-resume property production relies on). Tests that need distinct
// timestamps pass their own content.
var baseTime = time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)

// Member is one simulated federation member.
type Member struct {
	Name string
	Node string // MagicDNS-style node name (== Name in the harness)
	Root string // temp dir; the FSBackend root (objects/, meta/ live here)

	fs   *backend.FSBackend
	down atomic.Bool
	mu   sync.Mutex // serializes backend access — models a single client over one
	// SSH/taildrive channel (the single-active-writer assumption, Q3/D12). This is
	// what makes WAL-as-lock arbitration deterministic: FSBackend's Put is not
	// create-exclusive under genuine parallelism (two racers can both rename, last
	// wins), so production's "one client" model is reproduced by serializing here
	// rather than relying on the filesystem.

	// Auth state for the §16 gate seam (opt-in; default = unprotected → ungated, so
	// data scenarios need no password). SetPassword/ClearPassword toggle these; the
	// gate consults them via Fed.Verifier → cli.SetTestGateVerifier.
	protected bool
	pwSet     bool
	hf        auth.HashFile
}

// SetPassword marks the member as password-PROTECTED and sets pw as its node
// password (a real argon2id hash, in memory). Driving a §16-gated command against
// it with a wrong/absent candidate then refuses with TV-AUTH-01 through the REAL
// gateLocation→auth.Gate→Verify path (only the SSH transport is bypassed — the
// gate logic under test is unchanged). Idempotent.
func (m *Member) SetPassword(t *testing.T, pw string) {
	t.Helper()
	hf, err := auth.NewHashFile([]byte(pw))
	if err != nil {
		t.Fatalf("fedtest: hash password for %q: %v", m.Name, err)
	}
	m.hf = hf
	m.protected = true
	m.pwSet = true
}

// ClearPassword marks the member PROTECTED but with NO password set — modelling a
// gated node on which `vault passwd` was never run. A gated command against it
// refuses with TV-AUTH-01 (the ErrNoPassword path), never silently proceeds.
func (m *Member) ClearPassword(t *testing.T) {
	t.Helper()
	m.protected = true
	m.pwSet = false
	m.hf = auth.HashFile{}
}

// SetDown toggles unreachability. While down, the member's prober fails and
// every backend call errors like a dead node (TV-NODE, preflight-shaped) before
// any data moves — exercising the real down-node classification paths.
func (m *Member) SetDown(down bool) { m.down.Store(down) }

// IsDown reports the current simulated reachability.
func (m *Member) IsDown() bool { return m.down.Load() }

// Backend returns the member's backend HONORING SetDown (errors while down). Use
// this anywhere production code would hold the member's backend.
func (m *Member) Backend() backend.Backend { return &downBackend{m: m} }

// catalog reads the member's current catalog (nil if none yet).
func (m *Member) catalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.Load(filepath.Join(m.Root, "meta", "catalog.toml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("fedtest: load catalog on %q: %v", m.Name, err)
	}
	return cat
}

// Fed is the harness: members + their shared roster + the seams internal/fed,
// gc, ops and verify accept.
type Fed struct {
	Members  []*Member
	Roster   fed.Roster
	FedID    string
	CacheDir string // per-test ~/.tailvault stand-in (caches, receipts)

	byName map[string]*Member
}

// New builds an N-member federation: a deterministic fed_id, each member an
// FSBackend over a fresh t.TempDir(), every member's catalog seeded with the
// shared [federation] roster (active, joined at baseTime). All members start up.
//
// SAME-NODE / CLI mode (GOAL.md 7b): New sandboxes the process environment
// (XDG_CONFIG_HOME + HOME → fresh temp dirs via t.Setenv) and REGISTERS every
// member as a taildrive location in the sandboxed locations.toml, so the harness
// members are resolvable by the REAL CLI — a test can drive
// `newRootCmd().Execute()` (vault init/put/mv/rm/sync-mode/get/heal/…) against a
// member and assert on real on-disk blobs/catalog/WAL + real exit codes. Members
// are always-reachable local-FS nodes (no Tailscale). The stub seams
// (Probe/Querier/Resolver/SetDown) remain for the pure-stub federation tests.
func New(t *testing.T, names ...string) *Fed {
	t.Helper()
	if len(names) == 0 {
		t.Fatal("fedtest.New: need at least one member name")
	}
	// Sandbox the user-level config + home so the real CLI (locations.toml,
	// receipts, caches) resolves to per-test temp dirs, never the developer's.
	cacheDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cacheDir)
	t.Setenv("HOME", t.TempDir())

	f := &Fed{
		FedID:    "fed-" + shortHash(names...),
		CacheDir: cacheDir,
		byName:   make(map[string]*Member, len(names)),
	}
	members := make([]catalog.Member, 0, len(names))
	reg, err := locations.Load() // empty under the fresh sandboxed XDG
	if err != nil {
		t.Fatalf("fedtest: load locations: %v", err)
	}
	for _, name := range names {
		root := t.TempDir()
		m := &Member{Name: name, Node: name, Root: root, fs: backend.NewFSBackend(root)}
		f.Members = append(f.Members, m)
		f.byName[name] = m
		members = append(members, catalog.Member{Name: name, Node: name, JoinedAt: baseTime, Status: catalog.StatusActive})
		// Register the member as a taildrive (local-FS) location so `tailvault
		// vault …` resolves it through the real registry.
		if err := reg.Add(name, locations.Location{Node: name, BasePath: root, Backend: locations.BackendTaildrive, Share: "vault"}); err != nil {
			t.Fatalf("fedtest: register location %q: %v", name, err)
		}
	}
	if err := reg.Save(); err != nil {
		t.Fatalf("fedtest: save locations: %v", err)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	f.Roster = fed.Roster{FedID: f.FedID, Members: members}

	// Seed each member's catalog with the federation header (empty file list).
	for _, m := range f.Members {
		cat := &catalog.Catalog{
			Version:    catalog.SchemaVersion,
			VaultName:  m.Name,
			Node:       m.Node,
			Federation: catalog.Federation{FedID: f.FedID, Members: members},
		}
		writeCatalog(t, m, cat)
	}
	return f
}

// lookup returns the named member or nil (the t-less getter the gate verifier
// seam uses).
func (f *Fed) lookup(name string) *Member { return f.byName[name] }

// Verifier is the gate seam the cli auth test installs via
// cli.SetTestGateVerifier(f.Verifier): it returns the in-memory auth.Verifier for
// a PROTECTED member so a §16-gated command runs the REAL gate path against it.
// An unprotected member returns (nil, false) → the production SSH/no-op logic
// stands (ungated). A protected-but-no-password member returns a verifier on the
// ErrNoPassword path. Wire it from the CONSUMING package (cli) — NOT from inside
// fedtest — so fedtest never imports cli (which would cycle):
//
//	cli.SetTestGateVerifier(f.Verifier); t.Cleanup(func() { cli.SetTestGateVerifier(nil) })
func (f *Fed) Verifier(name string) (auth.Verifier, bool) {
	m := f.lookup(name)
	if m == nil || !m.protected {
		return nil, false // never protected → ungated (production logic stands)
	}
	if !m.pwSet {
		return auth.MemoryVerifier{Set: false}, true // ErrNoPassword path
	}
	return auth.MemoryVerifier{HF: m.hf, Set: true}, true
}

// Member returns the named member (fatal if unknown).
func (f *Fed) Member(t *testing.T, name string) *Member {
	t.Helper()
	m, ok := f.byName[name]
	if !ok {
		t.Fatalf("fedtest: no such member %q", name)
	}
	return m
}

// Seed ingests a manual-mode file into member through the REAL ingest pipeline
// (ingest.Bootstrap: hash → WAL intent (genesis) → catalog upsert → WAL done),
// returning its catalog.File (self-certifying id included). The bytes live at the
// member's vault root (manual-file model).
func (f *Fed) Seed(t *testing.T, member, path string, content []byte) catalog.File {
	t.Helper()
	return f.seed(t, member, path, content, nil)
}

// SeedCrash ingests path on member through the REAL pipeline but aborts at the
// named step ("after-intent" | "after-bytes" | "after-catalog"), leaving genuine
// torn on-disk state for crash-recovery scenarios: a written WAL intent without
// its done marker (after-intent/after-bytes), or a flushed catalog whose done
// marker was never written (after-catalog). The blob bytes are on disk either
// way. Returns the file id of the interrupted ingest (read back from the pending
// WAL intent) so the caller can drive verify/ops/ReplayOp recovery.
func (f *Fed) SeedCrash(t *testing.T, member, path string, content []byte, failAt string) string {
	t.Helper()
	m := f.Member(t, member)
	abs := filepath.Join(m.Root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		t.Fatal(err)
	}
	cat := m.catalog(t)
	if cat == nil {
		t.Fatalf("fedtest: member %q has no catalog (New not run?)", member)
	}
	catPath := filepath.Join(m.Root, "meta", "catalog.toml")
	plan := ingest.Plan{Root: m.Root, Files: []ingest.Candidate{{Rel: path, Size: int64(len(content)), ModTime: baseTime}}}
	err := ingest.Bootstrap(context.Background(), ingest.BootstrapOpts{
		Root: m.Root, Node: m.Node, Actor: "fedtest",
		Log: &wal.Log{B: m.fs}, Cat: cat, CatPath: catPath, Plan: plan,
		Now: func() time.Time { return baseTime },
		Hook: func(step string) error {
			if step == failAt {
				return fmt.Errorf("fedtest: simulated crash at %q", step)
			}
			return nil
		},
	})
	if err == nil {
		t.Fatalf("fedtest: SeedCrash expected an abort at %q, but the seed completed", failAt)
	}
	// Recover the interrupted op id from the pending WAL intent.
	pend, perr := (&wal.Log{B: m.fs}).Pending(context.Background(), "")
	if perr != nil {
		t.Fatalf("fedtest: SeedCrash read pending: %v", perr)
	}
	for _, r := range pend {
		if r.Entry.Args["path"] == path {
			return r.Entry.OpID
		}
	}
	t.Fatalf("fedtest: SeedCrash left no pending intent for %q", path)
	return ""
}

// SeedGit seeds a git-mode object: the blob is Put into the member's objects/<sha>
// store, ingested through the WAL with sync_mode=git, and a matching v2 lock entry
// (id + embedded genesis) is appended to lk so heal/pull scenarios have a repo
// side. Returns the catalog.File.
func (f *Fed) SeedGit(t *testing.T, member, path string, content []byte, lk *lock.Lock) catalog.File {
	t.Helper()
	m := f.Member(t, member)
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	// The blob lives in objects/<sha> for git mode (push/pull transfer it).
	put(t, m, "objects/"+sha, content)
	file := f.seed(t, member, path, content, map[string]string{"sync_mode": catalog.SyncModeGit})
	if lk != nil {
		g := identity.Genesis{
			ContentSHA256: file.Genesis.ContentSHA256,
			OriginalPath:  file.Genesis.OriginalPath,
			IngestOpID:    file.Genesis.IngestOpID,
			OriginNode:    file.Genesis.OriginNode,
		}
		lk.Entries = append(lk.Entries, lock.Entry{
			Path:     path,
			ID:       file.ID,
			Genesis:  &g,
			SHA256:   file.SHA256,
			Size:     file.Size,
			Location: member,
			PushedAt: baseTime,
			Pusher:   "fedtest",
		})
		if lk.Version == 0 {
			lk.Version = lock.SchemaVersion
		}
	}
	return file
}

// seed runs a one-file ingest.Bootstrap against the member's real WAL+catalog.
// extraArgs overrides ingest args (e.g. sync_mode=git). The file content is
// written to the member root so Bootstrap's hashFile sees real bytes.
func (f *Fed) seed(t *testing.T, member, path string, content []byte, extraArgs map[string]string) catalog.File {
	t.Helper()
	m := f.Member(t, member)
	abs := filepath.Join(m.Root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		t.Fatal(err)
	}
	cat := m.catalog(t)
	if cat == nil {
		t.Fatalf("fedtest: member %q has no catalog (New not run?)", member)
	}
	catPath := filepath.Join(m.Root, "meta", "catalog.toml")
	plan := ingest.Plan{Root: m.Root, Files: []ingest.Candidate{{Rel: path, Size: int64(len(content)), ModTime: baseTime}}}
	if err := ingest.Bootstrap(context.Background(), ingest.BootstrapOpts{
		Root: m.Root, Node: m.Node, Actor: "fedtest",
		Log: &wal.Log{B: m.fs}, Cat: cat, CatPath: catPath, Plan: plan,
		Now: func() time.Time { return baseTime },
	}); err != nil {
		t.Fatalf("fedtest: seed %q on %q: %v", path, member, err)
	}
	// For a git-mode override, rewrite the freshly-ingested row's sync_mode (the
	// bootstrap pipeline always stamps manual; the override is harness-only).
	if extraArgs["sync_mode"] == catalog.SyncModeGit {
		cat = m.catalog(t)
		frow, ok := cat.Find(path)
		if !ok {
			t.Fatalf("fedtest: seeded git file %q missing from catalog", path)
		}
		frow.SyncMode = catalog.SyncModeGit
		cat.Upsert(frow)
		writeCatalog(t, m, cat)
	}
	out := m.catalog(t)
	file, ok := out.Find(path)
	if !ok {
		t.Fatalf("fedtest: seeded file %q missing from catalog", path)
	}
	return file
}

// BackendFor is the fed.BackendFor seam mapping a roster member to its backend.
// It honors SetDown (a down member's backend errors), so resolution/gc/ops see a
// dead node exactly as in production.
func (f *Fed) BackendFor() fed.BackendFor {
	return func(m catalog.Member) (backend.Backend, error) {
		mem, ok := f.byName[m.Name]
		if !ok {
			return nil, tserr.ConfigErr("fedtest: member "+m.Name+" not in harness", nil)
		}
		return mem.Backend(), nil
	}
}

// Querier returns the production fed.Querier (BackendQuerier) over the harness.
func (f *Fed) Querier() fed.Querier { return fed.NewBackendQuerier(f.BackendFor()) }

// Probe returns the reachability seam: a down member errors (TV-NODE), an up
// member answers nil. This is the authoritative reachability signal the resolver
// uses (D26: live pings decide, caches only color).
func (f *Fed) Probe() func(ctx context.Context, m catalog.Member) error {
	return func(_ context.Context, m catalog.Member) error {
		mem, ok := f.byName[m.Name]
		if !ok {
			return tserr.NodeOfflineErr(m.Node, nil)
		}
		if mem.down.Load() {
			return tserr.NodeOfflineErr(m.Node, nil)
		}
		return nil
	}
}

// Resolver assembles the production resolution engine over the harness (roster +
// querier + probe), the same wiring the CLI uses.
func (f *Fed) Resolver() *fed.Resolver {
	return &fed.Resolver{Roster: f.Roster, Q: f.Querier(), Probe: f.Probe()}
}

// Tamper flips a byte in member's WAL slot entry at seq k, breaking the hash
// chain so wal.Read returns ErrChainBroken — the tamper-detection fixture.
func (f *Fed) Tamper(t *testing.T, member string, k int) {
	t.Helper()
	m := f.Member(t, member)
	p := filepath.Join(m.Root, "meta", "wal", walSlotName(k))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("fedtest: tamper read WAL slot %d on %q: %v", k, member, err)
	}
	if len(b) == 0 {
		t.Fatalf("fedtest: WAL slot %d on %q is empty", k, member)
	}
	// Flip the case of the first ASCII letter at/after the midpoint: a letter→letter
	// change keeps the file valid TOML (Decode still succeeds) while changing the
	// entry's self-hash → the chain breaks at the NEXT link (ErrChainBroken). A
	// single-entry WAL anchors only on seq/prev_hash (left intact), so tamper the
	// FIRST of ≥2 entries to trip verification.
	mutated := append([]byte(nil), b...)
	flipped := false
	for i := len(mutated) / 2; i < len(mutated); i++ {
		if c := mutated[i]; (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			mutated[i] ^= 0x20
			flipped = true
			break
		}
	}
	if !flipped {
		t.Fatalf("fedtest: WAL slot %d on %q has no letter to flip", k, member)
	}
	if err := os.WriteFile(p, mutated, 0o644); err != nil {
		t.Fatalf("fedtest: tamper write WAL slot %d on %q: %v", k, member, err)
	}
}

// --- internal helpers ---

// downBackend wraps a member's FSBackend: it short-circuits every call with a
// TV-NODE error while the member is down (before any data moves), and serializes
// all calls through the member mutex so the harness models a single client over
// one channel (deterministic WAL-as-lock arbitration; see Member.mu).
type downBackend struct{ m *Member }

func (d *downBackend) errIfDown() error {
	if d.m.down.Load() {
		return tserr.NodeOfflineErr(d.m.Node, nil)
	}
	return nil
}

func (d *downBackend) Stat(ctx context.Context, key string) (backend.Meta, error) {
	if err := d.errIfDown(); err != nil {
		return backend.Meta{}, err
	}
	d.m.mu.Lock()
	defer d.m.mu.Unlock()
	return d.m.fs.Stat(ctx, key)
}

func (d *downBackend) Get(ctx context.Context, key string, w io.Writer) error {
	if err := d.errIfDown(); err != nil {
		return err
	}
	d.m.mu.Lock()
	defer d.m.mu.Unlock()
	return d.m.fs.Get(ctx, key, w)
}

func (d *downBackend) Put(ctx context.Context, key string, r io.Reader) error {
	if err := d.errIfDown(); err != nil {
		return err
	}
	d.m.mu.Lock()
	defer d.m.mu.Unlock()
	return d.m.fs.Put(ctx, key, r)
}

func (d *downBackend) PutOverwrite(ctx context.Context, key string, r io.Reader) error {
	if err := d.errIfDown(); err != nil {
		return err
	}
	d.m.mu.Lock()
	defer d.m.mu.Unlock()
	return d.m.fs.PutOverwrite(ctx, key, r)
}

func (d *downBackend) Delete(ctx context.Context, key string) error {
	if err := d.errIfDown(); err != nil {
		return err
	}
	d.m.mu.Lock()
	defer d.m.mu.Unlock()
	return d.m.fs.Delete(ctx, key)
}

func (d *downBackend) List(ctx context.Context, prefix string) ([]string, error) {
	if err := d.errIfDown(); err != nil {
		return nil, err
	}
	d.m.mu.Lock()
	defer d.m.mu.Unlock()
	return d.m.fs.List(ctx, prefix)
}

func (d *downBackend) HashObject(ctx context.Context, key string) (string, error) {
	if err := d.errIfDown(); err != nil {
		return "", err
	}
	d.m.mu.Lock()
	defer d.m.mu.Unlock()
	return d.m.fs.HashObject(ctx, key)
}

func writeCatalog(t *testing.T, m *Member, cat *catalog.Catalog) {
	t.Helper()
	p := filepath.Join(m.Root, "meta", "catalog.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := catalog.WriteAtomic(p, cat); err != nil {
		t.Fatalf("fedtest: write catalog on %q: %v", m.Name, err)
	}
}

func put(t *testing.T, m *Member, key string, content []byte) {
	t.Helper()
	if err := m.fs.Put(context.Background(), key, bytes.NewReader(content)); err != nil {
		t.Fatalf("fedtest: put %q on %q: %v", key, m.Name, err)
	}
}

// walSlotName mirrors wal's slot key format: meta/wal/<seq12>.toml.
func walSlotName(seq int) string { return fmt.Sprintf("%012d.toml", seq) }

func shortHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}
