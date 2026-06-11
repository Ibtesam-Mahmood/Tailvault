package wal

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func genesisEntry() Entry {
	return Entry{
		Seq:       0,
		OpID:      "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6",
		PrevHash:  ZeroHash,
		OpType:    OpIngest,
		BlobRefs:  []string{"30092d830e2641b447745655bbe4171675720a1aa8cf80e0ae3736e6e43111f0"},
		Actor:     "ibte@laptop",
		CreatedAt: time.Date(2026, 6, 11, 9, 10, 0, 0, time.UTC),
		Args: map[string]string{
			"sync_mode":      "manual",
			"path":           "pnp/board.pdf",
			"content_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			"origin_node":    "home-pi",
		},
	}
}

// The genesis entry encodes byte-identically to the published fixture and hashes
// to the frozen WAL test vector (SPEC v2 §10 references this).
func TestCanonicalEncodeAndHashVector(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "wal-genesis.toml"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Encode(genesisEntry())
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("encode not byte-identical to fixture:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	const wantHash = "bb55bed553d0ba5a797d2dbca8a041a073b7481fea5cf5fcb4f735979793cbc3"
	h, err := Hash(genesisEntry())
	if err != nil {
		t.Fatal(err)
	}
	if h != wantHash {
		t.Errorf("hash vector drifted: got %s want %s", h, wantHash)
	}
}

func TestEncodeDeterministicAndRoundTrip(t *testing.T) {
	e := genesisEntry()
	b1, _ := Encode(e)
	b2, _ := Encode(e)
	if string(b1) != string(b2) {
		t.Error("Encode not deterministic")
	}
	got, err := Decode(b1)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.OpID != e.OpID || got.Seq != e.Seq || got.OpType != e.OpType ||
		len(got.BlobRefs) != 1 || got.BlobRefs[0] != e.BlobRefs[0] ||
		got.Args["path"] != "pnp/board.pdf" || !got.CreatedAt.Equal(e.CreatedAt) {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestNewOpIDFormat(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewOpID()
		if !re.MatchString(id) {
			t.Fatalf("op id not 32 lowercase hex: %q", id)
		}
		// UUIDv4: version nibble (char 12) == '4', variant nibble (char 16) in 8..b.
		if id[12] != '4' {
			t.Fatalf("op id version nibble != 4: %q", id)
		}
		if c := id[16]; c != '8' && c != '9' && c != 'a' && c != 'b' {
			t.Fatalf("op id variant nibble not 8..b: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate op id: %q", id)
		}
		seen[id] = true
	}
}
