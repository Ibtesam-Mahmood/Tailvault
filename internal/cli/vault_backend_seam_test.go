package cli

import (
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
)

// TestBackendForLocation_TestSeam verifies the down-member backend seam's safety
// contract: nil seam = production construction unchanged; an installed seam's
// override is returned; a declining seam (false) falls back to production. This is
// the nil-seam-is-production property qa-review gates on — the seam can never
// alter production behavior, only redirect under an explicit test install.
func TestBackendForLocation_TestSeam(t *testing.T) {
	loc := locations.Location{Backend: locations.BackendTaildrive, BasePath: t.TempDir(), Share: "s"}

	// nil seam → production taildrive.
	b, err := backendForLocation(loc, "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.(*backend.Taildrive); !ok {
		t.Fatalf("nil seam must build the production taildrive, got %T", b)
	}

	// Installed seam → its override for the matching name.
	want := backend.NewTaildrive(t.TempDir())
	SetTestBackendFor(func(name string) (backend.Backend, bool) {
		if name == "x" {
			return want, true
		}
		return nil, false // decline others
	})
	t.Cleanup(func() { SetTestBackendFor(nil) })

	got, err := backendForLocation(loc, "x")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("installed seam must return the override for %q", "x")
	}
	// A declined name falls back to production construction.
	other, err := backendForLocation(loc, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := other.(*backend.Taildrive); !ok || other == want {
		t.Errorf("a declined seam (false) must fall back to production, got %T", other)
	}
}
