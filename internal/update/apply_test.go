package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// makeArchive builds a .tar.gz containing a single regular file named "tailvault"
// with the given contents, mirroring a GoReleaser archive.
func makeArchive(t *testing.T, binContents []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "tailvault", Mode: 0o755, Size: int64(len(binContents)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binContents); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("archive-bytes")
	name := "tailvault_0.0.106_linux_amd64.tar.gz"
	sums := fmt.Sprintf("%s  %s\n%s  other.tar.gz\n", sha256hex(data), name, sha256hex([]byte("x")))

	if err := verifyChecksum(data, name, []byte(sums)); err != nil {
		t.Errorf("matching checksum should pass: %v", err)
	}
	if err := verifyChecksum([]byte("tampered"), name, []byte(sums)); err == nil {
		t.Error("mismatched checksum should fail")
	}
	if err := verifyChecksum(data, "absent.tar.gz", []byte(sums)); err == nil {
		t.Error("missing checksum entry should fail")
	}
	// "*" binary-mode marker on the filename column is tolerated.
	starSums := fmt.Sprintf("%s *%s\n", sha256hex(data), name)
	if err := verifyChecksum(data, name, []byte(starSums)); err != nil {
		t.Errorf("star-prefixed filename should match: %v", err)
	}
}

func TestExtractBinary(t *testing.T) {
	want := []byte("#!/fake/elf binary contents")
	got, err := extractBinary(makeArchive(t, want))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("extracted %q, want %q", got, want)
	}

	// An archive without a tailvault entry must error, not return empty bytes.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "README", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	if _, err := extractBinary(buf.Bytes()); err == nil {
		t.Error("archive without tailvault should error")
	}
}

func TestReplaceExecutable(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "tailvault")
	if err := os.WriteFile(dest, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := []byte("new binary")
	if err := replaceExecutable(dest, newBin); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newBin) {
		t.Errorf("after replace, dest = %q, want %q", got, newBin)
	}
	info, _ := os.Stat(dest)
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("replaced binary is not executable: %v", info.Mode())
	}
	// No leftover staging temp files.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected only the dest file, got %d entries", len(entries))
	}
}

func TestApplyEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("self-update unsupported on windows")
	}
	binContents := []byte("the new tailvault binary")
	archive := makeArchive(t, binContents)
	archName := ArchiveName("0.0.106", runtime.GOOS, runtime.GOARCH)
	sums := fmt.Sprintf("%s  %s\n", sha256hex(archive), archName)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/asset/archive":
			w.Write(archive)
		case "/asset/sums":
			w.Write([]byte(sums))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rel := Release{
		Tag: "v0.0.106",
		Assets: []Asset{
			{Name: archName, APIURL: srv.URL + "/asset/archive"},
			{Name: "checksums.txt", APIURL: srv.URL + "/asset/sums"},
		},
	}

	dir := t.TempDir()
	dest := filepath.Join(dir, "tailvault")
	if err := os.WriteFile(dest, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	cl := &Client{HTTP: srv.Client()}
	if err := cl.Apply(context.Background(), rel, "0.0.106", dest); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, binContents) {
		t.Errorf("dest not updated: got %q", got)
	}
}

func TestApplyRejectsTamperedArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("self-update unsupported on windows")
	}
	archName := ArchiveName("0.0.106", runtime.GOOS, runtime.GOARCH)
	// checksums.txt claims a hash that the served (tampered) archive won't match.
	sums := fmt.Sprintf("%s  %s\n", sha256hex([]byte("expected")), archName)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sums" {
			w.Write([]byte(sums))
			return
		}
		w.Write([]byte("TAMPERED")) // archive whose hash differs
	}))
	defer srv.Close()

	rel := Release{Tag: "v0.0.106", Assets: []Asset{
		{Name: archName, APIURL: srv.URL + "/arch"},
		{Name: "checksums.txt", APIURL: srv.URL + "/sums"},
	}}
	dir := t.TempDir()
	dest := filepath.Join(dir, "tailvault")
	os.WriteFile(dest, []byte("old"), 0o755)

	cl := &Client{HTTP: srv.Client()}
	if err := cl.Apply(context.Background(), rel, "0.0.106", dest); err == nil {
		t.Fatal("Apply should reject a checksum mismatch")
	}
	// dest must be left untouched on a rejected update.
	if got, _ := os.ReadFile(dest); string(got) != "old" {
		t.Errorf("dest was modified despite failed verification: %q", got)
	}
}
