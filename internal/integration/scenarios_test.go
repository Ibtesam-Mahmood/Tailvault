//go:build integration

package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/pull"
	"github.com/Ibtesam-Mahmood/tailvault/internal/push"
	"github.com/Ibtesam-Mahmood/tailvault/internal/revert"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/verify"
)

//  1. Hard-fail: an unreachable node aborts push with a node bucket code, leaving
//     no blobs on the node and no committed lock.
func TestScenario_HardFail_NodeDown(t *testing.T) {
	e := newEnv(t, false)
	e.write("a.bin", "alpha")
	down := func(context.Context) error { return tserr.NodeOfflineErr("home-pi", errors.New("down")) }

	_, err := e.push(down, push.Options{})
	var te *tserr.Error
	if !errors.As(err, &te) || te.ExitCode() != 4 {
		t.Fatalf("hard-fail: want TV-NODE exit 4, got %v", err)
	}
	if e.nodeObjectCount() != 0 {
		t.Errorf("node has %d blobs after a failed push; want 0 (no partial upload)", e.nodeObjectCount())
	}
	if _, err := os.Stat(filepath.Join(e.root, "tailvault.lock")); !os.IsNotExist(err) {
		t.Error("lock was written despite node-down; must stay unadvanced")
	}
}

// 2. Dedup: re-pushing an unchanged tree transfers nothing.
func TestScenario_Dedup(t *testing.T) {
	e := newEnv(t, false)
	e.write("a.bin", "alpha")

	r1, err := e.push(okPreflight, push.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Uploaded) != 1 {
		t.Fatalf("first push uploaded %v, want 1", r1.Uploaded)
	}
	r2, err := e.push(okPreflight, push.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// An unchanged tree transfers nothing (push's same-path/same-sha no-op path).
	if len(r2.Uploaded) != 0 {
		t.Errorf("re-push uploaded=%v, want 0 (zero transfer on unchanged tree)", r2.Uploaded)
	}
	if e.nodeObjectCount() != 1 {
		t.Errorf("node object count = %d, want 1 (no new blob)", e.nodeObjectCount())
	}
}

// 3. Move/rename: same content at a new path is zero-transfer, lock key renamed.
func TestScenario_MoveRename(t *testing.T) {
	e := newEnv(t, false)
	e.write("a.bin", "shared")
	if _, err := e.push(okPreflight, push.Options{}); err != nil {
		t.Fatal(err)
	}
	objectsBefore := e.nodeObjectCount()

	e.remove("a.bin")
	e.write("b.bin", "shared") // same content, new path
	r, err := e.push(okPreflight, push.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Uploaded) != 0 || len(r.Renamed) != 1 {
		t.Errorf("rename uploaded=%v renamed=%v, want 0 uploaded / 1 renamed", r.Uploaded, r.Renamed)
	}
	if e.nodeObjectCount() != objectsBefore {
		t.Errorf("rename changed node object count %d->%d, want unchanged", objectsBefore, e.nodeObjectCount())
	}
	lk := e.loadLock()
	if _, ok := lk.Find("b.bin"); !ok {
		t.Error("lock missing renamed key b.bin")
	}
	if _, ok := lk.Find("a.bin"); ok {
		t.Error("lock still has old key a.bin")
	}
}

//  4. Delete + auto_delete + preserve: deleting a plain file makes its blob
//     GC-eligible (auto_delete), while deleting a *preserve* file keeps a
//     tombstone so its blob SURVIVES the sweep (DESIGN §4). The tombstone keeps
//     the sha in gc's keep-set + preserve-set even though the file is gone.
func TestScenario_DeleteAutoDeleteAndPreserve(t *testing.T) {
	e := newEnv(t, false)
	e.cfg.Rules.Overrides = []config.Override{{Match: "keep.bin", Preserve: true}}
	e.write("drop.bin", "droppable")
	e.write("keep.bin", "keepsake")
	if _, err := e.push(okPreflight, push.Options{}); err != nil {
		t.Fatal(err)
	}
	e.commitLock("add both")
	dropSha, keepSha := shaOf("droppable"), shaOf("keepsake")

	// Delete BOTH files from the tree, then push.
	e.remove("drop.bin")
	e.remove("keep.bin")
	r, err := e.push(okPreflight, push.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Only the plain blob is marked for GC; the preserve file becomes a tombstone.
	if len(r.MarkedGC) != 1 || r.MarkedGC[0] != dropSha {
		t.Errorf("MarkedGC = %v, want only %s (keep.bin tombstoned, not marked)", r.MarkedGC, dropSha)
	}
	e.commitLock("delete both")

	// The lock retains keep.bin as a tombstone (Deleted=true) and drops drop.bin.
	lk := e.loadLock()
	if ke, ok := lk.Find("keep.bin"); !ok || !ke.Deleted || ke.SHA256 != keepSha {
		t.Errorf("keep.bin tombstone = %+v (ok=%v), want Deleted=true sha=%s", ke, ok, keepSha)
	}
	if _, ok := lk.Find("drop.bin"); ok {
		t.Error("drop.bin entry should be dropped (not tombstoned)")
	}

	// Sweep: the plain blob is reclaimed; the preserved blob survives.
	plan := e.gcSweep(false)
	for _, s := range plan.Eligible {
		if s == keepSha {
			t.Error("preserve blob is GC-eligible after deletion — must be kept (DESIGN §4)")
		}
	}
	if e.nodeHas(dropSha) {
		t.Error("dropped plain blob should be swept (auto_delete)")
	}
	if !e.nodeHas(keepSha) {
		t.Error("preserve blob must survive the sweep even after the file is deleted")
	}
}

// 4b. auto_delete = off: a deleted plain file is tombstoned (not GC-marked) so its
//
//	blob survives — the other half of the tombstone predicate (team-lead asked
//	both deletion-survival cases be named: preserve-delete AND auto_delete-off).
func TestScenario_AutoDeleteOff_DeleteKeepsBlob(t *testing.T) {
	e := newEnv(t, false)
	e.cfg.Rules.AutoDelete = false
	e.write("a.bin", "data")
	if _, err := e.push(okPreflight, push.Options{}); err != nil {
		t.Fatal(err)
	}
	e.commitLock("add a")
	sha := shaOf("data")

	e.remove("a.bin")
	r, err := e.push(okPreflight, push.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.MarkedGC) != 0 {
		t.Errorf("auto_delete off: MarkedGC = %v, want none (deletion tombstoned)", r.MarkedGC)
	}
	e.commitLock("delete a")

	lk := e.loadLock()
	if ae, ok := lk.Find("a.bin"); !ok || !ae.Deleted {
		t.Errorf("a.bin should be tombstoned (Deleted=true), got %+v ok=%v", ae, ok)
	}
	e.gcSweep(false)
	if !e.nodeHas(sha) {
		t.Error("auto_delete-off deleted file's blob must survive (tombstone keeps it)")
	}
}

// 4c. A deleted preserve file must NOT be resurrected by a fresh clone + pull: the
//
//	tombstone keeps the blob alive, but the file stays deleted (qa-review check [4]).
func TestScenario_PreserveDelete_NoResurrectionOnPull(t *testing.T) {
	e := newEnv(t, false)
	e.cfg.Rules.Overrides = []config.Override{{Match: "keep.bin", Preserve: true}}
	e.write("keep.bin", "keepsake")
	if _, err := e.push(okPreflight, push.Options{}); err != nil {
		t.Fatal(err)
	}
	e.remove("keep.bin")
	if _, err := e.push(okPreflight, push.Options{}); err != nil {
		t.Fatal(err)
	}
	lk := e.loadLock() // carries the keep.bin tombstone

	// Simulate a fresh clone: a new empty working tree with only the lock.
	clone := t.TempDir()
	if _, err := pull.Run(e.ctx, clone, lk, pull.Deps{Backend: e.be, Preflight: okPreflight}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clone, "keep.bin")); !os.IsNotExist(err) {
		t.Error("pull resurrected a tombstoned (deleted) preserve file — it must stay deleted")
	}
}

//  5. Per-branch GC: a blob dropped on branch A survives because branch B's
//     committed lock still references it.
func TestScenario_PerBranchGC(t *testing.T) {
	e := newEnv(t, false)
	e.write("a.bin", "shared")
	if _, err := e.push(okPreflight, push.Options{}); err != nil {
		t.Fatal(err)
	}
	e.commitLock("main: add a")
	sha := shaOf("shared")

	// Branch feature keeps a.bin@shared in its committed lock.
	git(t, e.root, "branch", "feature")

	// On main, delete a.bin and push+commit — main's lock drops the sha.
	e.remove("a.bin")
	if _, err := e.push(okPreflight, push.Options{}); err != nil {
		t.Fatal(err)
	}
	e.commitLock("main: delete a")

	// Sweep: union(main={}, feature={sha}) keeps the blob.
	plan := e.gcSweep(false)
	for _, s := range plan.Eligible {
		if s == sha {
			t.Errorf("blob %s swept but branch feature still references it", sha)
		}
	}
	if !e.nodeHas(sha) {
		t.Error("cross-branch blob must survive (in feature's keep-set)")
	}
}

//  6. History + revert: a history-on file with two versions reverts to the older
//     blob, restoring both the working bytes and the lock sha.
func TestScenario_HistoryRevert(t *testing.T) {
	e := newEnv(t, true) // history on
	e.write("a.bin", "v1")
	if _, err := e.push(okPreflight, push.Options{}); err != nil {
		t.Fatal(err)
	}
	e.write("a.bin", "v2")
	if _, err := e.push(okPreflight, push.Options{}); err != nil {
		t.Fatal(err)
	}
	v1 := shaOf("v1")

	if err := revert.Run(e.ctx, revert.Options{RepoRoot: e.root, Path: "a.bin", SHA: v1, Backend: e.be}); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if got := e.read("a.bin"); got != "v1" {
		t.Errorf("working file = %q, want v1", got)
	}
	lk := e.loadLock()
	entry, _ := lk.Find("a.bin")
	if entry.SHA256 != v1 {
		t.Errorf("lock sha = %s, want %s (v1)", entry.SHA256, v1)
	}
}

//  7. Integrity: a corrupted stored blob is reported corrupt; a missing one is
//     reported missing.
func TestScenario_Integrity(t *testing.T) {
	e := newEnv(t, false)
	e.write("a.bin", "data")
	if _, err := e.push(okPreflight, push.Options{}); err != nil {
		t.Fatal(err)
	}
	sha := shaOf("data")
	blobPath := filepath.Join(e.node, "objects", sha)

	// Corrupt the stored bytes (they no longer hash to the key).
	if err := os.WriteFile(blobPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := verify.Run(e.ctx, e.be, e.loadLock())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Corrupt) != 1 || rep.Corrupt[0].Key != sha {
		t.Errorf("verify corrupt = %v, want one finding for %s", rep.Corrupt, sha)
	}

	// Remove the blob entirely → missing (the lock still references it).
	if err := os.Remove(blobPath); err != nil {
		t.Fatal(err)
	}
	rep2, err := verify.Run(e.ctx, e.be, e.loadLock())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep2.Missing) != 1 || rep2.Missing[0].Key != sha {
		t.Errorf("verify missing = %v, want one finding for %s", rep2.Missing, sha)
	}
}
