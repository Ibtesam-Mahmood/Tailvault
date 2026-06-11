package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// writeBareVault writes an initialised-but-UNFEDERATED catalog (no [federation]
// section) — the precondition for `fed init` / `fed join`.
func writeBareVault(t *testing.T, dir string, files []catalog.File) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	cat := &catalog.Catalog{Version: catalog.SchemaVersion, VaultName: "v", Node: "n", Files: files}
	if err := catalog.WriteAtomic(filepath.Join(dir, "meta", "catalog.toml"), cat); err != nil {
		t.Fatalf("write bare vault: %v", err)
	}
}

// fedMembers reads a member dir's roster.
func fedMembers(t *testing.T, dir string) []catalog.Member {
	t.Helper()
	cat, err := catalog.Load(filepath.Join(dir, "meta", "catalog.toml"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return cat.Federation.Members
}

func memberStatus(members []catalog.Member, name string) (string, bool) {
	for _, m := range members {
		if m.Name == name {
			return m.Status, true
		}
	}
	return "", false
}

func fedID(t *testing.T, dir string) string {
	t.Helper()
	cat, err := catalog.Load(filepath.Join(dir, "meta", "catalog.toml"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return cat.Federation.FedID
}

func TestFedInit(t *testing.T) {
	dirs := newFed(t, "home-pi")
	writeBareVault(t, dirs["home-pi"], nil)

	out, err := run("fed", "init", "home-pi")
	if err != nil {
		t.Fatalf("fed init: %v\n%s", err, out)
	}
	id := fedID(t, dirs["home-pi"])
	if len(id) != 64 {
		t.Errorf("fed_id = %q, want 64-hex", id)
	}
	members := fedMembers(t, dirs["home-pi"])
	if len(members) != 1 || members[0].Name != "home-pi" || members[0].Status != catalog.StatusActive {
		t.Errorf("roster = %+v; want one active home-pi", members)
	}
	// WAL: a done roster op records the init.
	recs, _ := (&wal.Log{B: backend.NewTaildrive(dirs["home-pi"])}).Read(context.Background())
	ok := false
	for _, r := range recs {
		if r.Entry.OpType == wal.OpRoster && r.State == wal.StateDone && r.Entry.Args["action"] == "init" {
			ok = true
		}
	}
	if !ok {
		t.Error("missing done roster init WAL record")
	}

	// Re-init is refused (already federated).
	if _, err := run("fed", "init", "home-pi"); !isTVCode(err, tserr.ConfigBad) {
		t.Fatalf("re-init: want TV-CFG-01, got %v", err)
	}
}

func TestFedJoin(t *testing.T) {
	dirs := newFed(t, "home-pi", "office-nas")
	writeBareVault(t, dirs["home-pi"], nil)
	writeBareVault(t, dirs["office-nas"], nil)

	if _, err := run("fed", "init", "home-pi"); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, err := run("fed", "join", "office-nas")
	if err != nil {
		t.Fatalf("join: %v\n%s", err, out)
	}

	// Both members' rosters carry both, active, with the SAME fed_id.
	if fedID(t, dirs["home-pi"]) != fedID(t, dirs["office-nas"]) {
		t.Error("fed_id must match across members")
	}
	for _, d := range []string{dirs["home-pi"], dirs["office-nas"]} {
		ms := fedMembers(t, d)
		if s, ok := memberStatus(ms, "home-pi"); !ok || s != catalog.StatusActive {
			t.Errorf("%s roster missing active home-pi: %+v", d, ms)
		}
		if s, ok := memberStatus(ms, "office-nas"); !ok || s != catalog.StatusActive {
			t.Errorf("%s roster missing active office-nas: %+v", d, ms)
		}
	}

	// Idempotent: re-join is a no-op success.
	if _, err := run("fed", "join", "office-nas"); err != nil {
		t.Errorf("re-join must be a no-op success, got %v", err)
	}
}

func TestFedLeave(t *testing.T) {
	dirs := newFed(t, "home-pi", "office-nas")
	content := []byte("leaver data\n")
	f := realFile(t, dirs["office-nas"], "media/a.txt", content, content, catalog.SyncModeManual)
	writeFedVault(t, dirs, "home-pi", nil)
	writeFedVault(t, dirs, "office-nas", []catalog.File{f})

	out, err := run("fed", "leave", "office-nas")
	if err != nil {
		t.Fatalf("leave: %v\n%s", err, out)
	}
	// office-nas marked left across BOTH rosters; its row is kept, not removed.
	for _, d := range []string{dirs["home-pi"], dirs["office-nas"]} {
		if s, ok := memberStatus(fedMembers(t, d), "office-nas"); !ok || s != catalog.StatusLeft {
			t.Errorf("%s: office-nas status = %q, want left (row kept)", d, s)
		}
	}
	// No data deleted — the leaver's file + blob are intact.
	if _, ok := mvReadCat(t, dirs["office-nas"]).Find("media/a.txt"); !ok {
		t.Error("leave must not delete the leaver's catalog entries")
	}
	if _, err := os.Stat(filepath.Join(dirs["office-nas"], "objects", f.SHA256)); err != nil {
		t.Error("leave must not delete the leaver's blobs")
	}
	// The repo-facing consequence is printed loudly.
	if !strings.Contains(out, "WARN on next pull") || !strings.Contains(out, "No data was deleted") {
		t.Errorf("leave must print the repush/resync consequence:\n%s", out)
	}
}

func TestFedEvict_RefusesLiveMember(t *testing.T) {
	dirs := newFed(t, "home-pi", "office-nas")
	writeFedVault(t, dirs, "home-pi", nil)
	writeFedVault(t, dirs, "office-nas", nil)

	// Both members are reachable (taildrive), so evict must refuse.
	_, err := run("fed", "evict", "office-nas")
	if !isTVCode(err, tserr.ConfigBad) {
		t.Fatalf("evict of a reachable member: want TV-CFG-01, got %v", err)
	}
	// Roster untouched.
	if s, _ := memberStatus(fedMembers(t, dirs["home-pi"]), "office-nas"); s != catalog.StatusActive {
		t.Errorf("a refused evict must leave the roster active, got %q", s)
	}
}

func TestFedStatus(t *testing.T) {
	dirs := newFed(t, "home-pi", "office-nas")
	writeFedVault(t, dirs, "home-pi", nil)
	writeFedVault(t, dirs, "office-nas", nil)

	out, err := run("fed", "status", "--json")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	var s fedStatusJSON
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if s.FedID != "fed-1" || len(s.Rows) != 2 {
		t.Errorf("status = %+v; want fed-1 with 2 rows", s)
	}
	online := 0
	for _, r := range s.Rows {
		if r.Online {
			online++
		}
	}
	if online != 2 {
		t.Errorf("both taildrive members should read online, got %d", online)
	}
	if s.Divergent {
		t.Error("identical rosters must not be flagged divergent")
	}
}

func TestFedJoin_NoFederation(t *testing.T) {
	dirs := newFed(t, "home-pi")
	writeBareVault(t, dirs["home-pi"], nil)
	// No federated location registered → nothing to join.
	if _, err := run("fed", "join", "home-pi"); !isTVCode(err, tserr.ConfigBad) {
		t.Fatalf("join with no federation: want TV-CFG-01, got %v", err)
	}
}
