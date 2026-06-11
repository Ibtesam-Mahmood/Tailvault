package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/ingest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// newFed sandboxes the env and registers one taildrive location per member,
// returning member→dir. Catalogs are NOT written yet: seed objects with realFile,
// then call writeFedVault to write each member's catalog with the shared roster.
func newFed(t *testing.T, members ...string) map[string]string {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", cfg)
	dirs := map[string]string{}
	for _, n := range members {
		dirs[n] = t.TempDir()
	}
	if err := taildriveReg(dirs).Save(); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	return dirs
}

// fedRoster builds the shared active roster for all members of a federation.
func fedRoster(dirs map[string]string) []catalog.Member {
	names := make([]string, 0, len(dirs))
	for n := range dirs {
		names = append(names, n)
	}
	sort.Strings(names)
	roster := make([]catalog.Member, len(names))
	for i, n := range names {
		roster[i] = catalog.Member{Name: n, Node: n + ".ts", Status: catalog.StatusActive}
	}
	return roster
}

// writeFedVault writes member's catalog: the shared roster + its files.
func writeFedVault(t *testing.T, dirs map[string]string, member string, files []catalog.File) {
	t.Helper()
	writeMemberVault(t, dirs[member], "fed-1", fedRoster(dirs), files)
}

// mvReadCat loads a member's on-disk catalog.
func mvReadCat(t *testing.T, dir string) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.Load(filepath.Join(dir, "meta", "catalog.toml"))
	if err != nil {
		t.Fatalf("load catalog %s: %v", dir, err)
	}
	return cat
}

// mvMoveRecs returns the OpMove WAL records on a member.
func mvMoveRecs(t *testing.T, dir string) []wal.Rec {
	t.Helper()
	recs, err := (&wal.Log{B: backend.NewTaildrive(dir)}).Read(context.Background())
	if err != nil {
		t.Fatalf("read wal %s: %v", dir, err)
	}
	var out []wal.Rec
	for _, r := range recs {
		if r.Entry.OpType == wal.OpMove {
			out = append(out, r)
		}
	}
	return out
}

func TestVaultMv_IntraRename(t *testing.T) {
	dirs := newFed(t, "home-pi")
	content := []byte("intra payload\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})

	out, err := run("vault", "mv", "home-pi/media/a.txt", "home-pi/media/b.txt")
	if err != nil {
		t.Fatalf("intra mv: %v\n%s", err, out)
	}

	cat := mvReadCat(t, dirs["home-pi"])
	if _, ok := cat.Find("media/a.txt"); ok {
		t.Error("old path media/a.txt must be gone")
	}
	nf, ok := cat.Find("media/b.txt")
	if !ok {
		t.Fatal("new path media/b.txt missing")
	}
	if nf.ID != f.ID || nf.SHA256 != f.SHA256 {
		t.Errorf("intra rename must preserve id+sha: got id=%s sha=%s", nf.ID, nf.SHA256)
	}
	// The object never moved (same node).
	if _, err := os.Stat(filepath.Join(dirs["home-pi"], "objects", f.SHA256)); err != nil {
		t.Errorf("object should stay put on an intra rename: %v", err)
	}
	// WAL: one done move, no moved_to (same-member rename is not a forwarder).
	recs := mvMoveRecs(t, dirs["home-pi"])
	if len(recs) != 1 || recs[0].State != wal.StateDone {
		t.Fatalf("want 1 done move, got %+v", recs)
	}
	if recs[0].Entry.Args["moved_to"] != "" {
		t.Errorf("intra rename must NOT leave a moved_to forwarder: %v", recs[0].Entry.Args)
	}
	if recs[0].Entry.Args["from"] != "media/a.txt" || recs[0].Entry.Args["to"] != "media/b.txt" {
		t.Errorf("move args = %v", recs[0].Entry.Args)
	}
}

func TestVaultMv_IntraSelfMoveRefused(t *testing.T) {
	dirs := newFed(t, "home-pi")
	content := []byte("same\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})

	if _, err := run("vault", "mv", "home-pi/media/a.txt", "home-pi/media/a.txt"); !isTVCode(err, tserr.ConfigBad) {
		t.Fatalf("self-move: want TV-CFG-01, got %v", err)
	}
}

func TestVaultMv_CrossHappyPath(t *testing.T) {
	ctx := context.Background()
	dirs := newFed(t, "home-pi", "office-nas")
	content := []byte("cross payload\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})
	writeFedVault(t, dirs, "office-nas", nil) // dest initialised, empty

	out, err := run("vault", "mv", "home-pi/media/a.txt", "office-nas/clips/a.txt")
	if err != nil {
		t.Fatalf("cross mv: %v\n%s", err, out)
	}

	// Bytes landed at dest; dest catalog is live with the SAME id+sha.
	if _, err := os.Stat(filepath.Join(dirs["office-nas"], "objects", f.SHA256)); err != nil {
		t.Errorf("blob not at dest: %v", err)
	}
	dcat := mvReadCat(t, dirs["office-nas"])
	df, ok := dcat.Find("clips/a.txt")
	if !ok {
		t.Fatal("dest catalog missing clips/a.txt")
	}
	if df.ID != f.ID {
		t.Errorf("ID changed across move: %s -> %s", f.ID, df.ID)
	}
	if df.Genesis != f.Genesis {
		t.Errorf("genesis changed across move: %+v -> %+v", f.Genesis, df.Genesis)
	}
	if df.SHA256 != f.SHA256 {
		t.Errorf("sha changed: %s -> %s", f.SHA256, df.SHA256)
	}

	// Source catalog dropped the entry.
	if _, ok := mvReadCat(t, dirs["home-pi"]).Find("media/a.txt"); ok {
		t.Error("source catalog must drop the moved entry")
	}

	// WAL: source has a DONE move with moved_to=office-nas (the forwarding record);
	// dest has a DONE move with no forwarder.
	src := mvMoveRecs(t, dirs["home-pi"])
	if len(src) != 1 || src[0].State != wal.StateDone || src[0].Entry.Args["moved_to"] != "office-nas" {
		t.Fatalf("source move record = %+v; want 1 done with moved_to=office-nas", src)
	}
	dst := mvMoveRecs(t, dirs["office-nas"])
	if len(dst) != 1 || dst[0].State != wal.StateDone {
		t.Fatalf("dest move record = %+v; want 1 done", dst)
	}
	if dst[0].Entry.Args["moved_to"] != "" {
		t.Errorf("dest must not be a forwarder: %v", dst[0].Entry.Args)
	}
	// Projection-sufficiency (fix-35-D): the dest record is the file's ONLY trace on
	// the dest node, so it must carry the full genesis preimage for ProjectCatalog.
	da := dst[0].Entry.Args
	if da["id"] != f.ID || da["content_sha256"] != f.Genesis.ContentSHA256 ||
		da["original_path"] != f.Genesis.OriginalPath || da["ingest_op_id"] != f.Genesis.IngestOpID ||
		da["origin_node"] != f.Genesis.OriginNode || da["dest_path"] != "clips/a.txt" {
		t.Errorf("dest move record is not projection-sufficient: %v", da)
	}
	// sync_mode + size make the rebuilt row faithful (mode preserved, not §9c manual).
	if da["sync_mode"] != f.SyncMode || da["size"] == "" {
		t.Errorf("dest move record missing sync_mode/size for faithful rebuild: %v", da)
	}

	// The forwarding record is consumable: a querier hitting the OLD home reports
	// the file as moved_to the new home (this is what lets resolution find a file
	// whose new home is offline — the offline variant is exercised in task-50).
	q := fed.NewBackendQuerier(backendForRegistry(taildriveReg(dirs)))
	view, err := q.Query(ctx, catalog.Member{Name: "home-pi", Node: "home-pi.ts", Status: catalog.StatusActive}, f.ID)
	if err != nil {
		t.Fatalf("query source: %v", err)
	}
	if view.Found || view.MovedTo != "office-nas" {
		t.Errorf("source view = %+v; want Found=false MovedTo=office-nas", view)
	}
}

func TestVaultMv_ConflictCopy(t *testing.T) {
	dirs := newFed(t, "home-pi", "office-nas")
	content := []byte("dupe\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	existing := realFile(t, dirs["office-nas"], "media/a.txt", []byte("already here\n"), []byte("already here\n"), catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})
	writeFedVault(t, dirs, "office-nas", []catalog.File{existing})

	if _, err := run("vault", "mv", "home-pi/media/a.txt", "office-nas/media/a.txt", "--on-conflict=copy"); err != nil {
		t.Fatalf("mv copy: %v", err)
	}
	dcat := mvReadCat(t, dirs["office-nas"])
	if _, ok := dcat.Find("media/a.txt"); !ok {
		t.Error("existing dest entry must remain")
	}
	if _, ok := dcat.Find("media/a (2).txt"); !ok {
		t.Error("moved file should land under the deduped name media/a (2).txt")
	}
}

func TestVaultMv_ConflictStop(t *testing.T) {
	dirs := newFed(t, "home-pi", "office-nas")
	content := []byte("dupe\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	existing := realFile(t, dirs["office-nas"], "media/a.txt", []byte("already here\n"), []byte("already here\n"), catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})
	writeFedVault(t, dirs, "office-nas", []catalog.File{existing})

	before, _ := os.ReadFile(filepath.Join(dirs["office-nas"], "meta", "catalog.toml"))
	if _, err := run("vault", "mv", "home-pi/media/a.txt", "office-nas/media/a.txt", "--on-conflict=stop"); err != nil {
		t.Fatalf("mv stop must exit 0: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dirs["office-nas"], "meta", "catalog.toml"))
	if string(before) != string(after) {
		t.Error("stop must leave the dest catalog byte-identical")
	}
	// Source untouched too — nothing was moved.
	if _, ok := mvReadCat(t, dirs["home-pi"]).Find("media/a.txt"); !ok {
		t.Error("stop must leave the source entry in place")
	}
	if len(mvMoveRecs(t, dirs["home-pi"])) != 0 {
		t.Error("stop must not take any WAL intent")
	}
}

func TestVaultMv_ConflictRename(t *testing.T) {
	dirs := newFed(t, "home-pi", "office-nas")
	content := []byte("dupe\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	existing := realFile(t, dirs["office-nas"], "media/a.txt", []byte("already here\n"), []byte("already here\n"), catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})
	writeFedVault(t, dirs, "office-nas", []catalog.File{existing})

	if _, err := run("vault", "mv", "home-pi/media/a.txt", "office-nas/media/a.txt", "--on-conflict=rename", "--rename-to=media/b.txt"); err != nil {
		t.Fatalf("mv rename: %v", err)
	}
	if _, ok := mvReadCat(t, dirs["office-nas"]).Find("media/b.txt"); !ok {
		t.Error("renamed dest entry media/b.txt not found")
	}
}

func TestVaultMv_ConflictNoFlagNonTTY(t *testing.T) {
	dirs := newFed(t, "home-pi", "office-nas")
	content := []byte("dupe\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	existing := realFile(t, dirs["office-nas"], "media/a.txt", []byte("here\n"), []byte("here\n"), catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})
	writeFedVault(t, dirs, "office-nas", []catalog.File{existing})

	if _, err := runStdin("", "vault", "mv", "home-pi/media/a.txt", "office-nas/media/a.txt"); !isTVCode(err, tserr.ConfigBad) {
		t.Fatalf("conflict without --on-conflict in non-TTY: want TV-CFG-01, got %v", err)
	}
}

func TestVaultMv_ManualDriftRehash(t *testing.T) {
	dirs := newFed(t, "home-pi", "office-nas")
	scanned := []byte("scanned content\n")
	edited := []byte("edited since the last scan\n")
	// Recorded sha is sha256(scanned), but the stored object is the edited bytes:
	// a manual file changed in place since its last scan (legitimate drift, H12).
	f := realFile(t, dirs["home-pi"], "notes/m.txt", scanned, edited, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})
	writeFedVault(t, dirs, "office-nas", nil)

	if _, err := run("vault", "mv", "home-pi/notes/m.txt", "office-nas/notes/m.txt"); err != nil {
		t.Fatalf("manual drift mv: %v", err)
	}

	sum := sha256.Sum256(edited)
	freshSHA := hex.EncodeToString(sum[:])
	df, ok := mvReadCat(t, dirs["office-nas"]).Find("notes/m.txt")
	if !ok {
		t.Fatal("dest entry missing")
	}
	if df.SHA256 != freshSHA {
		t.Errorf("dest sha = %s, want the re-hash %s", df.SHA256, freshSHA)
	}
	if !df.LastScanned.After(f.LastScanned) {
		t.Errorf("last_scanned must advance on a drift move: %s !> %s", df.LastScanned, f.LastScanned)
	}
	// The bytes are content-addressed under the fresh hash so `vault get` resolves.
	if _, err := os.Stat(filepath.Join(dirs["office-nas"], "objects", freshSHA)); err != nil {
		t.Errorf("re-homed object missing at objects/%s: %v", freshSHA, err)
	}
	// ID is still invariant despite the content drift.
	if df.ID != f.ID {
		t.Errorf("drift move changed id: %s -> %s", f.ID, df.ID)
	}
}

func TestVaultMv_OpInFlight(t *testing.T) {
	ctx := context.Background()
	dirs := newFed(t, "home-pi", "office-nas")
	content := []byte("locked\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})
	writeFedVault(t, dirs, "office-nas", nil)

	// A pending (intent, never-done) move on the same blob on the source node.
	srcLog := &wal.Log{B: backend.NewTaildrive(dirs["home-pi"])}
	if _, err := srcLog.AppendIntent(ctx, wal.Entry{
		OpID: wal.NewOpID(), OpType: wal.OpMove, BlobRefs: []string{f.ID}, Actor: "other",
		CreatedAt: lastScan, Args: map[string]string{"from": "home-pi", "to": "office-nas"},
	}); err != nil {
		t.Fatalf("seed in-flight intent: %v", err)
	}

	if _, err := run("vault", "mv", "home-pi/media/a.txt", "office-nas/media/a.txt"); !isTVCode(err, tserr.ConfigBad) {
		t.Fatalf("second move while one is in flight: want TV-CFG-01 (op in flight), got %v", err)
	}
}

func TestVaultMv_DestNotInitialised(t *testing.T) {
	dirs := newFed(t, "home-pi", "office-nas")
	content := []byte("payload\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})
	// office-nas registered + in the roster but never initialised (no catalog).

	if _, err := run("vault", "mv", "home-pi/media/a.txt", "office-nas/media/a.txt"); !isTVCode(err, tserr.ConfigBad) {
		t.Fatalf("move to uninitialised dest: want TV-CFG-01, got %v", err)
	}
	// Nothing was written anywhere.
	if len(mvMoveRecs(t, dirs["home-pi"])) != 0 {
		t.Error("a doomed move must not take a source intent")
	}
}

func TestVaultMv_IdempotentResume(t *testing.T) {
	// Model a CRASHED cross move: the deterministic SOURCE intent was appended but
	// the move never completed (no transfer, no catalogs, no done). Re-running mv
	// re-presents the SAME deterministic op id → the source WAL dedups
	// (ErrDuplicateOp) and the command resumes to completion with NO duplicate
	// intent and NO second identity.
	ctx := context.Background()
	dirs := newFed(t, "home-pi", "office-nas")
	content := []byte("resume safe\n")
	f := realFile(t, dirs["home-pi"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})
	writeFedVault(t, dirs, "office-nas", nil)

	const destRel = "media/a.txt"
	opID := moveOpID(f.ID, "home-pi", "office-nas", destRel)
	srcLog := &wal.Log{B: backend.NewTaildrive(dirs["home-pi"])}
	if _, err := srcLog.AppendIntent(ctx, wal.Entry{
		OpID: opID, OpType: wal.OpMove, BlobRefs: []string{f.ID}, Actor: "crashed",
		CreatedAt: lastScan, Args: map[string]string{
			"from": "home-pi", "to": "office-nas", "moved_to": "office-nas",
			"src_path": "media/a.txt", "dest_path": destRel, "content_sha256": f.SHA256,
		},
	}); err != nil {
		t.Fatalf("seed interrupted intent: %v", err)
	}

	if _, err := run("vault", "mv", "home-pi/media/a.txt", "office-nas/"+destRel); err != nil {
		t.Fatalf("resume mv: %v", err)
	}

	// Exactly one source move record (the resumed one, now done) — no duplicate.
	src := mvMoveRecs(t, dirs["home-pi"])
	if len(src) != 1 || src[0].State != wal.StateDone || src[0].Entry.OpID != opID {
		t.Fatalf("resume produced %+v; want 1 done record with the original op id", src)
	}
	// Dest is live; source dropped the entry.
	if _, ok := mvReadCat(t, dirs["office-nas"]).Find(destRel); !ok {
		t.Error("resume must complete the dest catalog add")
	}
	if _, ok := mvReadCat(t, dirs["home-pi"]).Find("media/a.txt"); ok {
		t.Error("resume must complete the source demotion")
	}
}

// TestVaultMv_CrossMoveGitRebuildsFaithfully is the #44 (fix-35-D-resid) SG-8 gate:
// a GIT-mode file cross-moved via the REAL mvCross must, when the dest catalog is
// lost and rebuilt from its WAL alone (ingest.ProjectCatalog), come back as GIT
// (not the old §9c manual default) with its size, id, genesis, sha and path intact.
// A manual downgrade here would be an integrity-semantics regression: gc would
// exempt the file forever and verify would treat a corrupt blob as legitimate
// drift. The dest WAL's OpMove record is the file's ONLY trace on the dest, so it
// must journal sync_mode+size (a22efbb) — this drives the real writer→projector
// round-trip that the genesis-args-only assertion can't.
func TestVaultMv_CrossMoveGitRebuildsFaithfully(t *testing.T) {
	ctx := context.Background()
	dirs := newFed(t, "home-pi", "office-nas")
	content := []byte("git payload that moves across members\n")
	// A GIT-mode file with its blob at objects/<sha> on the source.
	f := realFile(t, dirs["home-pi"], "media/a.bin", content, content, catalog.SyncModeGit)
	writeFedVault(t, dirs, "home-pi", []catalog.File{f})
	writeFedVault(t, dirs, "office-nas", nil)

	if _, err := run("vault", "mv", "home-pi/media/a.bin", "office-nas/clips/a.bin"); err != nil {
		t.Fatalf("cross move: %v", err)
	}

	// Rebuild the dest catalog from its WAL alone (the `vault rebuild-catalog` path).
	destDir := dirs["office-nas"]
	recs, err := (&wal.Log{B: backend.NewTaildrive(destDir)}).Read(ctx)
	if err != nil {
		t.Fatalf("read dest wal: %v", err)
	}
	rebuilt, err := ingest.ProjectCatalog(mvReadCat(t, destDir), recs, "office-nas")
	if err != nil {
		t.Fatalf("project dest catalog: %v", err)
	}

	row, ok := rebuilt.Find("clips/a.bin")
	if !ok {
		t.Fatal("rebuilt catalog is missing the cross-moved file")
	}
	if row.SyncMode != catalog.SyncModeGit {
		t.Errorf("rebuilt sync_mode = %q, want git (a manual downgrade exempts a git file from gc forever)", row.SyncMode)
	}
	if row.Size != f.Size {
		t.Errorf("rebuilt size = %d, want %d", row.Size, f.Size)
	}
	if row.ID != f.ID || row.SHA256 != f.SHA256 || catalog.Genesis(row.Genesis) != f.Genesis {
		t.Errorf("rebuilt identity not preserved: id=%s sha=%s genesis=%+v", row.ID, row.SHA256, row.Genesis)
	}
	// The rebuilt row's id self-certifies against its projected genesis (tamper guard).
	g := identity.Genesis{
		ContentSHA256: row.Genesis.ContentSHA256, OriginalPath: row.Genesis.OriginalPath,
		IngestOpID: row.Genesis.IngestOpID, OriginNode: row.Genesis.OriginNode,
	}
	if ok, _ := identity.Verify(g, row.ID); !ok {
		t.Errorf("rebuilt cross-moved row id %s does not self-certify its genesis", row.ID)
	}
}
