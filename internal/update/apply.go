package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ArchiveName is the GoReleaser archive file name for a platform. It MUST match
// the `archives.name_template` in .goreleaser.yaml — the two are a contract.
// version is the tag without a leading "v" (e.g. "0.0.106").
func ArchiveName(version, goos, goarch string) string {
	return fmt.Sprintf("tailvault_%s_%s_%s.tar.gz", version, goos, goarch)
}

// SelfPath returns the absolute, symlink-resolved path of the running binary —
// the file `tailvault update` replaces in place.
func SelfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}

// Apply downloads the platform archive for rel, verifies it against the
// release's checksums.txt, extracts the tailvault binary, and atomically
// replaces destPath. version is rel.Tag without the leading "v".
//
// Self-replacement of a running executable is not supported on Windows (the OS
// locks it); there the caller is told to reinstall via the installer/package
// manager instead.
func (c *Client) Apply(ctx context.Context, rel Release, version, destPath string) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("in-place self-update is unsupported on Windows; re-run the installer or `go install ...@latest`")
	}
	archName := ArchiveName(version, runtime.GOOS, runtime.GOARCH)
	archAsset, ok := rel.asset(archName)
	if !ok {
		return fmt.Errorf("release %s has no asset %q for this platform", rel.Tag, archName)
	}
	sumAsset, ok := rel.asset("checksums.txt")
	if !ok {
		return fmt.Errorf("release %s has no checksums.txt — refusing to update unverified", rel.Tag)
	}

	archive, err := c.download(ctx, archAsset)
	if err != nil {
		return err
	}
	sums, err := c.download(ctx, sumAsset)
	if err != nil {
		return err
	}
	if err := verifyChecksum(archive, archName, sums); err != nil {
		return err
	}
	bin, err := extractBinary(archive)
	if err != nil {
		return err
	}
	return replaceExecutable(destPath, bin)
}

// verifyChecksum confirms data's SHA-256 matches the entry for name in a
// GoReleaser checksums.txt ("<hex>  <filename>" per line). A mismatch or a
// missing entry is a hard failure — tailvault never installs unverified bytes.
func verifyChecksum(data []byte, name string, checksums []byte) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		// The filename column may carry a leading "*" (binary mode marker).
		if strings.TrimPrefix(fields[1], "*") == name {
			if !strings.EqualFold(fields[0], got) {
				return fmt.Errorf("checksum mismatch for %s: archive sha256 %s does not match published %s", name, got, fields[0])
			}
			return nil
		}
	}
	return fmt.Errorf("no checksum entry for %s in checksums.txt", name)
}

// extractBinary pulls the "tailvault" executable out of a .tar.gz archive.
func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		if filepath.Base(hdr.Name) == "tailvault" && hdr.Typeflag == tar.TypeReg {
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read binary from archive: %w", err)
			}
			return b, nil
		}
	}
	return nil, fmt.Errorf("archive does not contain a tailvault binary")
}

// replaceExecutable atomically swaps the file at dest with newBin. It writes a
// temp file in the same directory (so the final rename stays on one filesystem),
// makes it executable, then renames over dest — leaving the old binary intact if
// any step before the rename fails.
func replaceExecutable(dest string, newBin []byte) error {
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, ".tailvault-update-*")
	if err != nil {
		return fmt.Errorf("cannot stage update in %s (need write access there): %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(newBin); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write staged binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close staged binary: %w", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("chmod staged binary: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		cleanup()
		return fmt.Errorf("replace %s (need write access there): %w", dest, err)
	}
	return nil
}
