package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// makeFile builds a catalog.File with a valid 64-hex ID and minimal genesis.
func makeFile(idHead, path string) catalog.File {
	id := idHead
	for len(id) < 64 {
		id += "0"
	}
	id = id[:64]
	return catalog.File{
		ID:       id,
		Genesis:  catalog.Genesis{ContentSHA256: id, OriginalPath: path, IngestOpID: "op", OriginNode: "n"},
		SHA256:   id,
		Path:     path,
		SyncMode: catalog.SyncModeManual,
		Size:     10,
	}
}

func writeMemberVault(t *testing.T, dir, fedID string, members []catalog.Member, files []catalog.File) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	cat := &catalog.Catalog{
		Version: catalog.SchemaVersion, VaultName: "v", Node: "n",
		Federation: catalog.Federation{FedID: fedID, Members: members},
		Files:      files,
	}
	if err := catalog.WriteAtomic(filepath.Join(dir, "meta", "catalog.toml"), cat); err != nil {
		t.Fatalf("write member vault: %v", err)
	}
}

func taildriveReg(dirs map[string]string) locations.Registry {
	locs := map[string]locations.Location{}
	for name, dir := range dirs {
		locs[name] = locations.Location{Node: name + ".ts", BasePath: dir, Backend: locations.BackendTaildrive, Share: name}
	}
	return locations.Registry{Locations: locs}
}

func isTVCode(err error, code tserr.Code) bool {
	var te *tserr.Error
	return errors.As(err, &te) && te.Code == code
}

func TestFileByPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	f := makeFile("30092d830e26", "media/a.pdf")
	writeMemberVault(t, dir, "fed-1", nil, []catalog.File{f})
	reg := taildriveReg(map[string]string{"home-pi": dir})

	got, home, err := fileByPath(ctx, reg, "home-pi", "media/a.pdf")
	if err != nil || home != "home-pi" || got.ID != f.ID {
		t.Fatalf("fileByPath = (%v, %q, %v); want %s at home-pi", got.ID, home, err, f.ID)
	}
	if _, _, err := fileByPath(ctx, reg, "home-pi", "no/such"); !isTVCode(err, tserr.ObjMissing) {
		t.Errorf("missing path: want TV-OBJ-01, got %v", err)
	}
	if _, _, err := fileByPath(ctx, reg, "ghost", "x"); !isTVCode(err, tserr.ConfigBad) {
		t.Errorf("unknown location: want TV-CFG-01, got %v", err)
	}
}

func TestFileByIDPrefix(t *testing.T) {
	ctx := context.Background()
	dirA, dirB := t.TempDir(), t.TempDir()
	members := []catalog.Member{
		{Name: "a", Node: "a.ts", Status: catalog.StatusActive},
		{Name: "b", Node: "b.ts", Status: catalog.StatusActive},
	}
	fa := makeFile("30092d830e26", "a.pdf")
	fb := makeFile("abcdef123456", "b.pdf")
	writeMemberVault(t, dirA, "fed-1", members, []catalog.File{fa})
	writeMemberVault(t, dirB, "fed-1", members, []catalog.File{fb})
	reg := taildriveReg(map[string]string{"a": dirA, "b": dirB})
	roster := fed.Roster{FedID: "fed-1", Members: members}

	got, home, err := fileByIDPrefix(ctx, reg, roster, "30092d830e26")
	if err != nil || home != "a" || got.ID != fa.ID {
		t.Fatalf("unique prefix = (%v, %q, %v); want fa at a", got.ID, home, err)
	}
	if _, _, err := fileByIDPrefix(ctx, reg, roster, "ffffffffffff"); !isTVCode(err, tserr.ObjMissing) {
		t.Errorf("unknown prefix: want TV-OBJ-01, got %v", err)
	}
}

func TestFileByIDPrefix_Ambiguous(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	members := []catalog.Member{{Name: "a", Node: "a.ts", Status: catalog.StatusActive}}
	// Two distinct IDs sharing the head "aaaaaa".
	f1 := makeFile("aaaaaa111111", "one")
	f2 := makeFile("aaaaaa222222", "two")
	writeMemberVault(t, dir, "fed-1", members, []catalog.File{f1, f2})
	reg := taildriveReg(map[string]string{"a": dir})
	roster := fed.Roster{FedID: "fed-1", Members: members}

	_, _, err := fileByIDPrefix(ctx, reg, roster, "aaaaaa")
	if !isTVCode(err, tserr.ConfigBad) {
		t.Errorf("ambiguous prefix: want TV-CFG-01 listing candidates, got %v", err)
	}
}
