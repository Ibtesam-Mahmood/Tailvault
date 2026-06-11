package catalog

import (
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

// Parse decodes catalog TOML bytes and validates them. It returns
// ErrIncompatibleVersion (wrapped) when the schema version is unknown so the
// command boundary can map it to exit 2.
func Parse(b []byte) (*Catalog, error) {
	var c Catalog
	if err := toml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("catalog: parse: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Encode renders the canonical catalog TOML: version forced to SchemaVersion,
// entries sorted by path byte-wise ascending, timestamps in UTC, fields in the
// frozen §9 declaration order. Two encodes of the same logical catalog produce
// identical bytes.
func Encode(c *Catalog) ([]byte, error) {
	c.Version = SchemaVersion
	c.Canonicalize()
	b, err := toml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("catalog: encode: %w", err)
	}
	return b, nil
}

// Load reads and parses a local catalog file.
func Load(path string) (*Catalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// WriteAtomic writes Encode(c) to path durably: a temp file in the SAME
// directory (cross-filesystem rename is not atomic), fsync the file, atomic
// rename over the target, then fsync the directory so the rename itself is
// durable. This is the single write seam for every local catalog mutation.
//
// Write-ahead ordering (SPEC v2 §10 / proposal Part II atomicity standards):
// callers MUST have written the referenced blob bytes BEFORE updating the
// catalog (WAL intent → blob bytes → catalog → WAL done). Do not invert it.
func WriteAtomic(path string, c *Catalog) error {
	b, err := Encode(c)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".catalog-*.tmp")
	if err != nil {
		return fmt.Errorf("catalog: create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename succeeds.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("catalog: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("catalog: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("catalog: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("catalog: rename: %w", err)
	}
	return fsyncDir(dir)
}

// fsyncDir flushes a directory entry so a rename into it survives a crash. A
// platform that cannot open a directory for sync is tolerated (best effort).
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil //nolint:nilerr // dir fsync is best-effort; the rename already landed
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return nil //nolint:nilerr // some filesystems reject dir fsync; tolerate it
	}
	return nil
}
