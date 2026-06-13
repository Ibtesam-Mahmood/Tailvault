package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
)

// A local location resolves to a real FSBackend whose bytes land on the local
// filesystem under base_path — proving the factory wiring end to end (the
// storage semantics themselves are already covered by every engine test, which
// runs against FSBackend).
func TestBackendForLocation_LocalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	b, err := backendForLocation(locations.Location{Backend: locations.BackendLocal, BasePath: dir}, "loc")
	if err != nil {
		t.Fatalf("backendForLocation(local): %v", err)
	}
	ctx := context.Background()

	if err := b.Put(ctx, "objects/abc123", strings.NewReader("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	var buf bytes.Buffer
	if err := b.Get(ctx, "objects/abc123", &buf); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if buf.String() != "hello" {
		t.Fatalf("round-trip mismatch: got %q", buf.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "objects", "abc123")); err != nil {
		t.Fatalf("blob did not land on the local disk: %v", err)
	}
}
