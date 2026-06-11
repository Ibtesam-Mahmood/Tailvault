package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fedtest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// TestFileByIDPrefix_PartialViewOnUnreachable (FED-LOOKUP-1): a zero-match id
// lookup must be reachability-aware. With every member up, an unknown id is a
// genuine TV-OBJ-01 miss (exit 5). With a member unreachable, a zero-match cannot
// prove federation-wide absence → TV-FED-01 PartialView (exit 6), never a silent
// miss. (Mirrors the resolver's review-32 safety property at the prefix-lookup
// layer that vault stat/get/mv resolve an id through.)
func TestFileByIDPrefix_PartialViewOnUnreachable(t *testing.T) {
	f := fedtest.New(t, "home", "office")
	SetTestBackendFor(func(name string) (backend.Backend, bool) {
		if b := f.MemberBackend(name); b != nil {
			return b, true
		}
		return nil, false
	})
	t.Cleanup(func() { SetTestBackendFor(nil) })

	held := f.Seed(t, "office", "media/clip.mp4", []byte("on office\n"))
	reg, err := locations.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	missing := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

	// All members up + an id no member holds → genuine miss (TV-OBJ-01, exit 5).
	var te *tserr.Error
	if _, _, err = fileByIDPrefix(ctx, reg, f.Roster, missing); !errors.As(err, &te) || te.Code != tserr.ObjMissing {
		t.Fatalf("genuine miss (all up) must be TV-OBJ-01, got %v", err)
	}
	if te.ExitCode() != 5 {
		t.Errorf("genuine miss exit = %d, want 5", te.ExitCode())
	}

	// Sole holder unreachable + lookup of an id only it holds → cannot prove absence
	// → TV-FED-01 PartialView (exit 6), never a silent TV-OBJ miss.
	f.Member(t, "office").SetDown(true)
	if _, _, err = fileByIDPrefix(ctx, reg, f.Roster, held.ID); !errors.As(err, &te) || te.Code != tserr.FedPartialView {
		t.Fatalf("down sole-holder must be TV-FED-01 PartialView, got %v", err)
	}
	if te.ExitCode() != 6 {
		t.Errorf("partial-view exit = %d, want 6", te.ExitCode())
	}

	// Even a genuinely-absent id is a partial view while a member is down: absence
	// is unprovable federation-wide, so never a silent miss.
	if _, _, err = fileByIDPrefix(ctx, reg, f.Roster, missing); !errors.As(err, &te) || te.Code != tserr.FedPartialView {
		t.Fatalf("any zero-match under a down member must be TV-FED-01, got %v", err)
	}
}
