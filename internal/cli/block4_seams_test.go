package cli

import (
	"context"
	"reflect"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
)

// TestSeam_NewTSClient_DefaultIsProduction proves the repo.go preflight seam is
// nil/default-is-production (review-50): the package default IS tailscale.New (the
// real client), and only an explicit reassignment overrides it — so a test that
// forgets to install/clean up still runs the production path, never a silent stub.
func TestSeam_NewTSClient_DefaultIsProduction(t *testing.T) {
	if reflect.ValueOf(newTSClient).Pointer() != reflect.ValueOf(tailscale.New).Pointer() {
		t.Fatal("newTSClient default must be tailscale.New (production) — only a test may reassign it")
	}
	if c := newTSClient(); c == nil {
		t.Fatal("default newTSClient() must return a real *tailscale.Client")
	}

	// An override is honored, and restoring the default returns to production.
	installed := false
	newTSClient = func() *tailscale.Client { installed = true; return &tailscale.Client{R: okTSRunner{}} }
	t.Cleanup(func() { newTSClient = tailscale.New })
	_ = newTSClient()
	if !installed {
		t.Fatal("reassigned newTSClient must be used")
	}
	newTSClient = tailscale.New
	if reflect.ValueOf(newTSClient).Pointer() != reflect.ValueOf(tailscale.New).Pointer() {
		t.Fatal("restoring newTSClient = tailscale.New must return to the production path")
	}
}

// TestSeam_GCProbe_NilIsProduction proves the gc.go reachability-probe seam is
// nil-is-production (review-50): when no override is installed (testGCProbe == nil)
// runFederatedGC uses the real ts.Ping path; an install is opt-in and clearable.
// A command that never installs the seam therefore pings for real — the stub can
// never silently weaken the all-members gate.
func TestSeam_GCProbe_NilIsProduction(t *testing.T) {
	// Clearing yields nil → the production ts.Ping branch in runFederatedGC.
	SetTestGCProbe(nil)
	if testGCProbe != nil {
		t.Fatal("SetTestGCProbe(nil) must leave the probe nil (production ts.Ping path)")
	}

	// Installing an override is honored.
	SetTestGCProbe(func(_ context.Context, _ catalog.Member) error { return nil })
	if testGCProbe == nil {
		t.Fatal("SetTestGCProbe(fn) must install the override")
	}

	// And it is clearable back to production.
	SetTestGCProbe(nil)
	if testGCProbe != nil {
		t.Fatal("SetTestGCProbe(nil) must restore the production path")
	}
}
