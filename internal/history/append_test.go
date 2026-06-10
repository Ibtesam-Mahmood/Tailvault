package history

import (
	"context"
	"reflect"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
)

func TestAppendVersion_NewestFirst(t *testing.T) {
	ctx := context.Background()
	b := backend.NewFSBackend(t.TempDir())
	id := PathID("masters/board.stl")

	for i, sha := range []string{"v1", "v2", "v3"} {
		got, err := AppendVersion(ctx, b, id, sha)
		if err != nil {
			t.Fatalf("AppendVersion %s: %v", sha, err)
		}
		if got[0] != sha {
			t.Errorf("after appending %s, head = %s (step %d)", sha, got[0], i)
		}
	}

	versions, err := ReadVersions(ctx, b, id)
	if err != nil {
		t.Fatalf("ReadVersions: %v", err)
	}
	want := []string{"v3", "v2", "v1"}
	if !reflect.DeepEqual(versions, want) {
		t.Errorf("versions = %v, want %v (newest-first)", versions, want)
	}
}

func TestAppendVersion_DedupHead(t *testing.T) {
	ctx := context.Background()
	b := backend.NewFSBackend(t.TempDir())
	id := PathID("a/b.pdf")

	if _, err := AppendVersion(ctx, b, id, "v1"); err != nil {
		t.Fatal(err)
	}
	// Re-pushing identical content (sha already at head) is a no-op.
	got, err := AppendVersion(ctx, b, id, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"v1"}) {
		t.Errorf("re-append of head should be a no-op, got %v", got)
	}
}

func TestReadVersions_MissingRefIsEmpty(t *testing.T) {
	ctx := context.Background()
	b := backend.NewFSBackend(t.TempDir())
	got, err := ReadVersions(ctx, b, PathID("never/pushed.pdf"))
	if err != nil {
		t.Fatalf("ReadVersions on missing ref: %v", err)
	}
	if got != nil {
		t.Errorf("missing ref should read as nil, got %v", got)
	}
}
