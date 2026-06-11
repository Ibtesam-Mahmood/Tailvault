package pointer

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	cases := []Pointer{
		{SHA256: "9f2b1c", Size: 41231873, Location: "home-pi"},
		{SHA256: "00", Size: 0, Location: "x"},
		{SHA256: "abcdef0123456789", Size: 9223372036854775807, Location: "office-nas"},
	}
	for _, p := range cases {
		got, err := Decode(Encode(p))
		if err != nil {
			t.Fatalf("Decode(Encode(%+v)): %v", p, err)
		}
		if got != p {
			t.Errorf("round-trip = %+v, want %+v", got, p)
		}
	}
}

func TestDecodeProposalSample(t *testing.T) {
	in := []byte("tailvault.v1\nsha256 9f2b1c\nsize 41231873\nlocation home-pi\n")
	p, err := Decode(in)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := Pointer{SHA256: "9f2b1c", Size: 41231873, Location: "home-pi"}
	if p != want {
		t.Errorf("Decode = %+v, want %+v", p, want)
	}
}

func TestEncodeExactBytes(t *testing.T) {
	got := Encode(Pointer{SHA256: "9f2b1c", Size: 41231873, Location: "home-pi"})
	want := []byte("tailvault.v1\nsha256 9f2b1c\nsize 41231873\nlocation home-pi\n")
	if !bytes.Equal(got, want) {
		t.Errorf("Encode = %q, want %q", got, want)
	}
}

func TestDecodeRejects(t *testing.T) {
	bad := map[string]string{
		"wrong magic v2":   "tailvault.v2\nsha256 ab\nsize 1\nlocation x\n",
		"foobar magic":     "foobar\nsha256 ab\nsize 1\nlocation x\n",
		"empty":            "",
		"missing size":     "tailvault.v1\nsha256 ab\nlocation x\n",
		"reordered keys":   "tailvault.v1\nsize 1\nsha256 ab\nlocation x\n",
		"size abc":         "tailvault.v1\nsha256 ab\nsize abc\nlocation x\n",
		"negative size":    "tailvault.v1\nsha256 ab\nsize -1\nlocation x\n",
		"blank sha":        "tailvault.v1\nsha256 \nsize 1\nlocation x\n",
		"uppercase sha":    "tailvault.v1\nsha256 AB\nsize 1\nlocation x\n",
		"blank location":   "tailvault.v1\nsha256 ab\nsize 1\nlocation \n",
		"extra trailing":   "tailvault.v1\nsha256 ab\nsize 1\nlocation x\nextra\n",
		"missing location": "tailvault.v1\nsha256 ab\nsize 1\n",
	}
	for name, s := range bad {
		t.Run(name, func(t *testing.T) {
			p, err := Decode([]byte(s))
			if err == nil {
				t.Fatalf("expected error, got %+v", p)
			}
			if p != (Pointer{}) {
				t.Errorf("expected zero Pointer on error, got %+v", p)
			}
		})
	}
}

func TestIsPointer(t *testing.T) {
	yes := [][]byte{
		Encode(Pointer{SHA256: "ab", Size: 1, Location: "x"}),
		[]byte("tailvault.v1"),
		[]byte("tailvault.v1\n"),
	}
	for _, d := range yes {
		if !IsPointer(d) {
			t.Errorf("IsPointer(%q) = false, want true", d)
		}
	}
	no := [][]byte{
		{},
		[]byte("random text\n"),
		{0xFF, 0xD8, 0xFF}, // JPEG header
		[]byte("tailvault.v2\n"),
		[]byte("xtailvault.v1\n"),
	}
	for _, d := range no {
		if IsPointer(d) {
			t.Errorf("IsPointer(%q) = true, want false", d)
		}
	}
}
