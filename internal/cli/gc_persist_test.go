package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
)

// SG-6: persisting the catalog over a backend must OVERWRITE in place. With the
// old Delete-then-Put this was non-atomic; with plain Put it would silently
// no-op the second write (Put dedups on an existing key). PutOverwrite fixes
// both — a second persist must win.
func TestPersistCatalogOverBackend_Overwrites(t *testing.T) {
	ctx := context.Background()
	be := backend.NewFSBackend(t.TempDir())
	persist := persistCatalogOverBackend(be)

	if err := persist(ctx, &catalog.Catalog{Version: catalog.SchemaVersion, VaultName: "v1", Node: "n"}); err != nil {
		t.Fatalf("persist #1: %v", err)
	}
	if err := persist(ctx, &catalog.Catalog{Version: catalog.SchemaVersion, VaultName: "v2", Node: "n"}); err != nil {
		t.Fatalf("persist #2: %v", err)
	}

	var buf bytes.Buffer
	if err := be.Get(ctx, "meta/catalog.toml", &buf); err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := catalog.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.VaultName != "v2" {
		t.Fatalf("persist did not overwrite: vault_name=%q, want v2", got.VaultName)
	}
}
