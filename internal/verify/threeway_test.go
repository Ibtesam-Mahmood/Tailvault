package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

var t0 = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

func shaStr(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

// manualFile builds a self-certifying catalog entry for a manual file.
func manualFile(t *testing.T, path, content string) catalog.File {
	t.Helper()
	g := identity.Genesis{ContentSHA256: shaStr(content), OriginalPath: path,
		IngestOpID: "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6", OriginNode: "home-pi"}
	id, _ := identity.MintID(g)
	return catalog.File{
		ID: id, Genesis: catalog.Genesis(g), SHA256: shaStr(content), Path: path,
		SyncMode: catalog.SyncModeManual, Size: int64(len(content)),
		CreatedAt: t0, UpdatedAt: t0, LastScanned: t0,
	}
}

// vault writes the given manual files to a temp root (mtime=t0) and returns the
// root, a matching catalog, and a log over an FSBackend at the root.
func vault(t *testing.T, files map[string]string) (string, *catalog.Catalog, *wal.Log) {
	t.Helper()
	root := t.TempDir()
	cat := &catalog.Catalog{Version: catalog.SchemaVersion, Node: "home-pi"}
	for p, c := range files {
		fp := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fp, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(fp, t0, t0); err != nil {
			t.Fatal(err)
		}
		cat.Upsert(manualFile(t, p, c))
	}
	return root, cat, &wal.Log{B: backend.NewFSBackend(root)}
}

func opt() Options { return Options{Now: func() time.Time { return t0 }} }

func kinds(fs []ThreeFinding) map[FindingKind]int {
	m := map[FindingKind]int{}
	for _, f := range fs {
		m[f.Kind]++
	}
	return m
}

func TestThreeWayCleanVault(t *testing.T) {
	root, cat, log := vault(t, map[string]string{"a.txt": "alpha"})
	fs, err := ThreeWay(context.Background(), root, nil, cat, log, opt())
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 0 || ExitCode(fs) != 0 {
		t.Fatalf("clean vault: %+v exit=%d", fs, ExitCode(fs))
	}
}

func TestThreeWayMissingOnDisk(t *testing.T) {
	root, cat, log := vault(t, map[string]string{"a.txt": "alpha"})
	os.Remove(filepath.Join(root, "a.txt"))
	fs, _ := ThreeWay(context.Background(), root, nil, cat, log, opt())
	if kinds(fs)[MissingOnDisk] != 1 || ExitCode(fs) != 5 {
		t.Fatalf("want 1 MissingOnDisk exit5, got %+v exit=%d", fs, ExitCode(fs))
	}
}

func TestThreeWayEditedSinceScan(t *testing.T) {
	root, cat, log := vault(t, map[string]string{"a.txt": "alpha"})
	// edit: different content + size, mtime advanced past last_scanned.
	p := filepath.Join(root, "a.txt")
	os.WriteFile(p, []byte("alpha-edited-longer"), 0o644)
	os.Chtimes(p, t0.Add(time.Hour), t0.Add(time.Hour))
	fs, _ := ThreeWay(context.Background(), root, nil, cat, log, opt())
	if kinds(fs)[EditedSinceScan] != 1 || ExitCode(fs) != 0 {
		t.Fatalf("want EditedSinceScan exit0, got %+v exit=%d", fs, ExitCode(fs))
	}
}

func TestThreeWayCorrupt(t *testing.T) {
	root, cat, log := vault(t, map[string]string{"a.txt": "alpha"})
	// corruption: same size (5), mtime restored to t0 → no edit signal, bytes differ.
	p := filepath.Join(root, "a.txt")
	os.WriteFile(p, []byte("ALPHA"), 0o644)
	os.Chtimes(p, t0, t0)
	fs, _ := ThreeWay(context.Background(), root, nil, cat, log, opt())
	if kinds(fs)[Corrupt] != 1 || ExitCode(fs) != 5 {
		t.Fatalf("want Corrupt exit5, got %+v exit=%d", fs, ExitCode(fs))
	}
}

func TestThreeWayGenesisInvalid(t *testing.T) {
	root, cat, log := vault(t, map[string]string{"a.txt": "alpha"})
	cat.Files[0].Genesis.OriginNode = "tampered" // breaks sha256(genesis)==id
	fs, _ := ThreeWay(context.Background(), root, nil, cat, log, opt())
	if kinds(fs)[GenesisInvalid] != 1 || ExitCode(fs) != 5 {
		t.Fatalf("want GenesisInvalid exit5, got %+v exit=%d", fs, ExitCode(fs))
	}
}

func TestThreeWayChainBroken(t *testing.T) {
	root, cat, log := vault(t, map[string]string{"a.txt": "alpha"})
	ctx := context.Background()
	// two chained entries; tamper the first so the second's prev_hash breaks.
	for _, b := range []string{"B1", "B2"} {
		if _, err := log.AppendIntent(ctx, wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpMove,
			BlobRefs: []string{b}, Actor: "a", CreatedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(root, "meta", "wal", "000000000000.toml")
	raw, _ := os.ReadFile(p)
	os.WriteFile(p, append(raw, []byte("\n")...), 0o644) // tamper
	fs, err := ThreeWay(ctx, root, nil, cat, log, opt())
	if err != nil {
		t.Fatal(err)
	}
	if kinds(fs)[ChainBroken] != 1 || ExitCode(fs) != 6 {
		t.Fatalf("want ChainBroken exit6, got %+v exit=%d", fs, ExitCode(fs))
	}
}

func TestThreeWayPendingSuppressesCorruption(t *testing.T) {
	root, cat, log := vault(t, map[string]string{"a.txt": "alpha"})
	ctx := context.Background()
	// a pending intent on a.txt's id
	if _, err := log.AppendIntent(ctx, wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpMove,
		BlobRefs: []string{cat.Files[0].ID}, Actor: "a", CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	// corrupt the file — but the pending op must suppress the Corrupt verdict.
	p := filepath.Join(root, "a.txt")
	os.WriteFile(p, []byte("ALPHA"), 0o644)
	os.Chtimes(p, t0, t0)
	fs, _ := ThreeWay(ctx, root, nil, cat, log, opt())
	if kinds(fs)[PendingOpState] != 1 || kinds(fs)[Corrupt] != 0 {
		t.Fatalf("pending must suppress corruption: %+v", fs)
	}
}

func TestThreeWayLockReconcile(t *testing.T) {
	root, cat, log := vault(t, map[string]string{"a.txt": "alpha"})
	// lock has a sha mismatch on a.txt + a lock-only entry; catalog has a.txt only.
	lk := &lock.Lock{Version: 1, Entries: []lock.Entry{
		{Path: "a.txt", SHA256: "deadbeef"},
		{Path: "ghost.txt", SHA256: "cafe"},
	}}
	fs, _ := ThreeWay(context.Background(), root, lk, cat, log, opt())
	k := kinds(fs)
	if k[FieldMismatch] != 1 || k[LockOnlyEntry] != 1 {
		t.Fatalf("want 1 FieldMismatch + 1 LockOnlyEntry, got %+v", fs)
	}
}

func TestThreeWayNilCatalogNoFindings(t *testing.T) {
	root, _, log := vault(t, map[string]string{"a.txt": "alpha"})
	fs, err := ThreeWay(context.Background(), root, nil, nil, log, opt())
	if err != nil || len(fs) != 0 {
		t.Fatalf("nil catalog + no WAL history → no 3-way findings, got %+v err %v", fs, err)
	}
}

// TestThreeWayLockIDMismatch: DG-38.1 — a federated lock entry whose id diverges
// from the catalog id for the same path is a FieldMismatch (two identities for
// one path), even when the sha agrees.
func TestThreeWayLockIDMismatch(t *testing.T) {
	root, cat, log := vault(t, map[string]string{"a.txt": "alpha"})
	diffID := "9999999999999999999999999999999999999999999999999999999999999999"
	lk := &lock.Lock{Version: 2, Entries: []lock.Entry{
		{Path: "a.txt", SHA256: cat.Files[0].SHA256, ID: diffID}, // sha agrees, id differs
	}}
	fs, _ := ThreeWay(context.Background(), root, lk, cat, log, opt())
	if kinds(fs)[FieldMismatch] != 1 || ExitCode(fs) != 5 {
		t.Fatalf("want 1 FieldMismatch (id divergence) exit5, got %+v exit=%d", fs, ExitCode(fs))
	}
}

// TestThreeWayLockIDMatchNoFinding: a lock id that matches the catalog id (sha
// also agreeing) yields no finding — guards against a false positive.
func TestThreeWayLockIDMatchNoFinding(t *testing.T) {
	root, cat, log := vault(t, map[string]string{"a.txt": "alpha"})
	lk := &lock.Lock{Version: 2, Entries: []lock.Entry{
		{Path: "a.txt", SHA256: cat.Files[0].SHA256, ID: cat.Files[0].ID},
	}}
	fs, _ := ThreeWay(context.Background(), root, lk, cat, log, opt())
	if kinds(fs)[FieldMismatch] != 0 {
		t.Fatalf("matching id+sha must not flag, got %+v", fs)
	}
}

// TestThreeWayLockEmptyIDSkipped: DG-35.1 — push does not yet populate id/genesis
// into lock entries, so an empty-id lock entry skips the id cross-check (only sha
// is compared).
func TestThreeWayLockEmptyIDSkipped(t *testing.T) {
	root, cat, log := vault(t, map[string]string{"a.txt": "alpha"})
	lk := &lock.Lock{Version: 2, Entries: []lock.Entry{
		{Path: "a.txt", SHA256: cat.Files[0].SHA256}, // empty id (not yet populated)
	}}
	fs, _ := ThreeWay(context.Background(), root, lk, cat, log, opt())
	if kinds(fs)[FieldMismatch] != 0 {
		t.Fatalf("empty-id lock entry must be skipped, got %+v", fs)
	}
}

// TestThreeWayCatalogMissingWithWAL: review-38 LOW-2 — a nil catalog on a vault
// that HAS committed (done) WAL history means the catalog is missing/torn → a
// CatalogMissing finding pointing at rebuild-catalog (exit 5).
func TestThreeWayCatalogMissingWithWAL(t *testing.T) {
	root, _, log := vault(t, map[string]string{"a.txt": "alpha"})
	ctx := context.Background()
	rec, err := log.AppendIntent(ctx, wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpMove,
		BlobRefs: []string{"x"}, Actor: "a", CreatedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.MarkDone(ctx, rec.Entry.OpID); err != nil {
		t.Fatal(err)
	}
	fs, err := ThreeWay(ctx, root, nil, nil, log, opt())
	if err != nil {
		t.Fatal(err)
	}
	if kinds(fs)[CatalogMissing] != 1 || ExitCode(fs) != 5 {
		t.Fatalf("want 1 CatalogMissing exit5, got %+v exit=%d", fs, ExitCode(fs))
	}
}

// TestThreeWayCatalogMissingNoWALBenign: a nil catalog with only a PENDING (not
// done) op is not yet a committed vault → no CatalogMissing finding.
func TestThreeWayCatalogMissingNoWALBenign(t *testing.T) {
	root, _, log := vault(t, map[string]string{"a.txt": "alpha"})
	ctx := context.Background()
	if _, err := log.AppendIntent(ctx, wal.Entry{OpID: wal.NewOpID(), OpType: wal.OpMove,
		BlobRefs: []string{"x"}, Actor: "a", CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	fs, err := ThreeWay(ctx, root, nil, nil, log, opt())
	if err != nil {
		t.Fatal(err)
	}
	if kinds(fs)[CatalogMissing] != 0 {
		t.Fatalf("a pending-only WAL must not flag a missing catalog, got %+v", fs)
	}
}
