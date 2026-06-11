package cli

import (
	"context"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
)

// stubResolver maps a file id to a canned resolution Result for heal tests.
type stubResolver map[string]fed.Result

func (s stubResolver) Resolve(_ context.Context, id, _ string) (fed.Result, error) {
	return s[id], nil
}

func healEntry(t *testing.T, path, loc string) lock.Entry {
	t.Helper()
	g := identity.Genesis{
		ContentSHA256: "abc",
		OriginalPath:  path,
		IngestOpID:    "op-" + path,
		OriginNode:    loc,
	}
	id, err := identity.MintID(g)
	if err != nil {
		t.Fatal(err)
	}
	return lock.Entry{Path: path, ID: id, Genesis: &g, SHA256: "sha-" + path, Location: loc}
}

func TestHealLock_FoundElsewhere_RepointsLocationOnly(t *testing.T) {
	e := healEntry(t, "a.pdf", "home-pi")
	origGenesis := *e.Genesis
	origSHA, origID := e.SHA256, e.ID
	lk := &lock.Lock{Version: lock.SchemaVersion, Entries: []lock.Entry{e}}

	r := stubResolver{e.ID: {
		Outcome: fed.FoundElsewhere,
		View:    fed.MemberView{Member: "office-nas", Found: true},
	}}
	healed, partial, missing, atHome, changed, err := healLock(context.Background(), lk, r, false)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || len(healed) != 1 || len(partial) != 0 || len(missing) != 0 || len(atHome) != 0 {
		t.Fatalf("buckets wrong: healed=%d partial=%d missing=%d atHome=%d changed=%v", len(healed), len(partial), len(missing), len(atHome), changed)
	}
	got := lk.Entries[0]
	if got.Location != "office-nas" {
		t.Errorf("location not repointed: %q", got.Location)
	}
	// Identity is immutable — id, genesis, sha must be byte-for-byte unchanged.
	if got.ID != origID || got.SHA256 != origSHA || got.Genesis == nil || *got.Genesis != origGenesis {
		t.Errorf("heal mutated identity fields: %+v", got)
	}
}

func TestHealLock_DryRun_WritesNothing(t *testing.T) {
	e := healEntry(t, "a.pdf", "home-pi")
	lk := &lock.Lock{Version: lock.SchemaVersion, Entries: []lock.Entry{e}}
	r := stubResolver{e.ID: {Outcome: fed.FoundElsewhere, View: fed.MemberView{Member: "office-nas", Found: true}}}

	healed, _, _, _, changed, err := healLock(context.Background(), lk, r, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(healed) != 1 || !changed {
		t.Fatalf("dry-run should still REPORT the change: healed=%d changed=%v", len(healed), changed)
	}
	if lk.Entries[0].Location != "home-pi" {
		t.Errorf("dry-run must not mutate the lock; location = %q", lk.Entries[0].Location)
	}
}

func TestHealLock_PartialViewUntouched(t *testing.T) {
	e := healEntry(t, "a.pdf", "home-pi")
	lk := &lock.Lock{Version: lock.SchemaVersion, Entries: []lock.Entry{e}}
	r := stubResolver{e.ID: {Outcome: fed.PartialView}}

	_, partial, _, _, changed, err := healLock(context.Background(), lk, r, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(partial) != 1 || changed {
		t.Fatalf("partial view must be reported and untouched: partial=%d changed=%v", len(partial), changed)
	}
	if lk.Entries[0].Location != "home-pi" {
		t.Errorf("partial-view entry was rewritten: %q", lk.Entries[0].Location)
	}
}

func TestHealLock_SkipsNonFederatedAndAtHome(t *testing.T) {
	fedE := healEntry(t, "a.pdf", "home-pi")
	plain := lock.Entry{Path: "b.pdf", SHA256: "x", Location: "home-pi"} // no ID
	lk := &lock.Lock{Version: lock.SchemaVersion, Entries: []lock.Entry{fedE, plain}}
	r := stubResolver{fedE.ID: {Outcome: fed.FoundAtHome, View: fed.MemberView{Member: "home-pi", Found: true}}}

	healed, partial, missing, atHome, changed, err := healLock(context.Background(), lk, r, false)
	if err != nil {
		t.Fatal(err)
	}
	if changed || len(healed) != 0 || len(partial) != 0 || len(missing) != 0 {
		t.Fatalf("nothing should change: healed=%d partial=%d missing=%d changed=%v", len(healed), len(partial), len(missing), changed)
	}
	if len(atHome) != 1 {
		t.Errorf("the federated at-home entry should be counted, atHome=%d", len(atHome))
	}
}
