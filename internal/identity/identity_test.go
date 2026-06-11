package identity

import (
	"strings"
	"testing"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// The SPEC v2 §11 worked example.
func vectorGenesis() Genesis {
	return Genesis{
		ContentSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		OriginalPath:  "pnp/board.pdf",
		IngestOpID:    "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6",
		OriginNode:    "home-pi",
	}
}

const vectorID = "30092d830e2641b447745655bbe4171675720a1aa8cf80e0ae3736e6e43111f0"

func TestSpecTestVector(t *testing.T) {
	wantBytes := `content_sha256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
original_path = "pnp/board.pdf"
ingest_op_id = "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6"
origin_node = "home-pi"
`
	cb, err := vectorGenesis().CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(cb) != wantBytes {
		t.Errorf("canonical bytes drifted:\n--- got ---\n%q\n--- want ---\n%q", cb, wantBytes)
	}
	id, err := MintID(vectorGenesis())
	if err != nil {
		t.Fatal(err)
	}
	if id != vectorID {
		t.Errorf("MintID = %s, want %s", id, vectorID)
	}
}

func TestMintIDDeterministicAndOrderIndependent(t *testing.T) {
	want, _ := MintID(vectorGenesis())
	for i := 0; i < 100; i++ {
		got, _ := MintID(vectorGenesis())
		if got != want {
			t.Fatalf("non-deterministic mint at %d: %s != %s", i, got, want)
		}
	}
	// Struct field literal order in source cannot matter (it is fixed in
	// CanonicalBytes); build with a different literal order to be sure.
	g := Genesis{OriginNode: "home-pi", IngestOpID: "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6",
		OriginalPath: "pnp/board.pdf", ContentSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}
	if got, _ := MintID(g); got != want {
		t.Errorf("field literal order changed id: %s", got)
	}
}

func TestUniqueness(t *testing.T) {
	base := vectorGenesis()

	idBase, _ := MintID(base)

	// Same content, different path → different id.
	p2 := base
	p2.OriginalPath = "pnp/other.pdf"
	if idP2, _ := MintID(p2); idP2 == idBase {
		t.Error("different path must mint a different id")
	}

	// Same content + path, different op id → different id.
	o2 := base
	o2.IngestOpID = "ffffffffffffffffffffffffffffffff"
	if idO2, _ := MintID(o2); idO2 == idBase {
		t.Error("different ingest op id must mint a different id")
	}
}

func TestVerifySelfCertifies(t *testing.T) {
	ok, err := Verify(vectorGenesis(), vectorID)
	if err != nil || !ok {
		t.Fatalf("Verify(matching) = %v, %v; want true", ok, err)
	}
	// Case-insensitive on the claimed id.
	if ok, _ := Verify(vectorGenesis(), "30092D830E2641B447745655BBE4171675720A1AA8CF80E0AE3736E6E43111F0"); !ok {
		t.Error("Verify should be case-insensitive on the claimed id")
	}
	// Perturb a record field → fails.
	bad := vectorGenesis()
	bad.OriginNode = "other"
	if ok, _ := Verify(bad, vectorID); ok {
		t.Error("Verify must fail when a record field is perturbed")
	}
	// Perturb the id → fails.
	if ok, _ := Verify(vectorGenesis(), "0000000000000000000000000000000000000000000000000000000000000000"); ok {
		t.Error("Verify must fail for a wrong id")
	}
}

func TestShort(t *testing.T) {
	if Short(vectorID) != "30092d830e26" {
		t.Errorf("Short = %s", Short(vectorID))
	}
	if Short("abc") != "abc" {
		t.Errorf("Short of short input changed it")
	}
}

func TestFromIngestEntry(t *testing.T) {
	e := wal.Entry{
		OpID:   "0192f3a4b5c6d7e8f9a0b1c2d3e4f5a6",
		OpType: wal.OpIngest,
		Args: map[string]string{
			"content_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			"path":           "pnp/board.pdf",
		},
	}
	g, err := FromIngestEntry(e, "home-pi")
	if err != nil {
		t.Fatalf("FromIngestEntry: %v", err)
	}
	if id, _ := MintID(g); id != vectorID {
		t.Errorf("extracted genesis minted %s, want %s", id, vectorID)
	}

	// Non-ingest op type errors.
	e.OpType = wal.OpMove
	if _, err := FromIngestEntry(e, "home-pi"); err == nil {
		t.Error("FromIngestEntry must reject a non-ingest op type")
	}
}

func TestReceiptRoundTripAndCertification(t *testing.T) {
	dir := t.TempDir()
	r := Receipt{
		ID:           vectorID,
		Genesis:      vectorGenesis(),
		Path:         "pnp/board.pdf",
		SHA256AtPull: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		PulledAt:     time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC),
		SourceNode:   "home-pi.tailnet-name.ts.net",
	}
	if err := WriteReceipt(dir, r); err != nil {
		t.Fatalf("WriteReceipt: %v", err)
	}
	got, err := ReadReceipt(dir, vectorID)
	if err != nil {
		t.Fatalf("ReadReceipt: %v", err)
	}
	if got.ID != r.ID || got.Path != r.Path || got.SourceNode != r.SourceNode ||
		!got.PulledAt.Equal(r.PulledAt) || got.Genesis != r.Genesis {
		t.Errorf("round-trip mismatch:\n got %+v\n want %+v", got, r)
	}

	list, err := ListReceipts(dir)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListReceipts: %d, %v", len(list), err)
	}

	// Re-pull overwrites (latest wins).
	r2 := r
	r2.SourceNode = "office-nas"
	if err := WriteReceipt(dir, r2); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadReceipt(dir, vectorID); got.SourceNode != "office-nas" {
		t.Errorf("re-pull did not overwrite: %s", got.SourceNode)
	}
	if list, _ := ListReceipts(dir); len(list) != 1 {
		t.Errorf("re-pull created a duplicate receipt: %d", len(list))
	}

	// A non-certifying genesis is refused.
	bad := r
	bad.Genesis.OriginNode = "tampered"
	if err := WriteReceipt(dir, bad); err == nil {
		t.Error("WriteReceipt must refuse a non-certifying genesis")
	}
}

func TestGenesisValidate(t *testing.T) {
	if err := vectorGenesis().Validate(); err != nil {
		t.Fatalf("valid genesis rejected: %v", err)
	}
	bad := vectorGenesis()
	bad.ContentSHA256 = "short"
	if err := bad.Validate(); err == nil {
		t.Error("Validate accepted a non-64-hex content sha")
	}
}

func TestEscapeBasic(t *testing.T) {
	// A path with a quote and backslash must escape, and round-trip determinism
	// must hold (the id depends on the exact bytes).
	g := vectorGenesis()
	g.OriginalPath = `a "weird"\path` + "\t"
	cb, _ := g.CanonicalBytes()
	want := "original_path = \"a \\\"weird\\\"\\\\path\\t\"\n"
	if got := string(cb); !strings.Contains(got, want) {
		t.Errorf("escaping wrong:\n got %q\n want substring %q", got, want)
	}
}
