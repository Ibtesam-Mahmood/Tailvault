package status

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/pointer"
	"github.com/Ibtesam-Mahmood/tailvault/internal/rules"
)

// ManagedFiles walks the working tree under root and returns the repo-relative
// (slash-separated) paths the rule engine considers vault-managed. The .git
// directory and tailvault's own metadata files are skipped.
func ManagedFiles(cfg *config.Config, root string) ([]string, error) {
	var managed []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == "tailvault.toml" || rel == "tailvault.lock" {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		dec, derr := rules.Evaluate(cfg, rel, info.Size())
		if derr != nil {
			return derr
		}
		if dec.Managed {
			managed = append(managed, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return managed, nil
}

// ScanTree hashes each managed file's content. If a working file is still a
// pointer (eager smudge not yet run), the sha recorded in the pointer is used
// rather than hashing the pointer text — otherwise it would read as drifted.
func ScanTree(root string, managed []string) (map[string]string, error) {
	out := make(map[string]string, len(managed))
	for _, rel := range managed {
		sha, err := hashFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		out[rel] = sha
	}
	return out, nil
}

// hashFile returns the content sha256, or the pointer's recorded sha when the
// file is a tailvault pointer. It peeks only a small prefix to sniff the pointer
// magic, so real blobs (up to ~1 GB) are streamed through the hasher rather than
// read into memory.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	const sniff = 256 // pointer files are a handful of short lines
	head := make([]byte, sniff)
	n, rerr := io.ReadFull(f, head)
	if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
		return "", rerr
	}
	head = head[:n]

	if pointer.IsPointer(head) {
		// Small file: read the rest and decode the recorded sha.
		rest, _ := io.ReadAll(f)
		if p, derr := pointer.Decode(append(head, rest...)); derr == nil {
			return p.SHA256, nil
		}
		// Not a valid pointer after all — fall through to hashing the bytes we
		// have plus the remainder.
		h := sha256.New()
		h.Write(head)
		h.Write(rest)
		return hex.EncodeToString(h.Sum(nil)), nil
	}

	h := sha256.New()
	h.Write(head)
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
