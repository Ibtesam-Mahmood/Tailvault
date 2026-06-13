package locations

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestValidate_Local(t *testing.T) {
	// base_path only → valid.
	if err := (Location{Backend: BackendLocal, BasePath: "/tmp/store"}).Validate(); err != nil {
		t.Fatalf("local base_path-only should validate, got %v", err)
	}
	// missing base_path → error.
	if err := (Location{Backend: BackendLocal}).Validate(); err == nil {
		t.Fatal("local without base_path should fail")
	}
	// stray node/user/share → error (caught, not ignored).
	for _, bad := range []Location{
		{Backend: BackendLocal, BasePath: "/s", Node: "pi"},
		{Backend: BackendLocal, BasePath: "/s", User: "ibte"},
		{Backend: BackendLocal, BasePath: "/s", Share: "sh"},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("local with stray field should fail: %+v", bad)
		}
	}
}

func TestCheck_Local(t *testing.T) {
	ctx := context.Background()

	// Existing directory → reachable.
	dir := t.TempDir()
	if r := Check(ctx, "loc", Location{Backend: BackendLocal, BasePath: dir}, nil); !r.Reachable {
		t.Errorf("existing dir should be reachable, got %+v", r)
	}

	// Not-yet-created path → reachable (created on first write).
	missing := filepath.Join(dir, "nope", "store")
	if r := Check(ctx, "loc", Location{Backend: BackendLocal, BasePath: missing}, nil); !r.Reachable {
		t.Errorf("missing path should be reachable (created on write), got %+v", r)
	}

	// Path that exists as a file (not a dir) → unreachable.
	f := filepath.Join(dir, "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := Check(ctx, "loc", Location{Backend: BackendLocal, BasePath: f}, nil); r.Reachable {
		t.Errorf("a non-directory path should be unreachable, got %+v", r)
	}
}
