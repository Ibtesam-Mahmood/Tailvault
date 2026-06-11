package identity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Receipt is one ~/.tailvault/receipts/<id>.toml (SPEC v2 §12) — written by
// every `vault get` so each download is an off-node identity backup (D24b).
// Receipts are advisory recovery artifacts, never authoritative state; a re-pull
// overwrites (latest pull wins).
type Receipt struct {
	ID           string    `toml:"id"`
	Genesis      Genesis   `toml:"genesis,inline"`
	Path         string    `toml:"path"` // logical path at pull time
	SHA256AtPull string    `toml:"sha256_at_pull"`
	PulledAt     time.Time `toml:"pulled_at"`
	SourceNode   string    `toml:"source_node"`
}

// DefaultReceiptDir is ~/.tailvault/receipts. Funcs take an explicit dir so
// tests can redirect; pass DefaultReceiptDir() in production.
func DefaultReceiptDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tailvault", "receipts"), nil
}

// WriteReceipt atomically writes a receipt to dir/<id>.toml. It refuses a
// receipt whose genesis does not self-certify its id — a corrupt recovery
// artifact is worse than none.
func WriteReceipt(dir string, r Receipt) error {
	ok, err := Verify(r.Genesis, r.ID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("identity: receipt genesis does not certify id %s", r.ID)
	}
	if !isHex64(strings.ToLower(r.ID)) {
		return fmt.Errorf("identity: receipt id is not 64 hex chars: %q", r.ID)
	}
	r.PulledAt = r.PulledAt.UTC()
	b, err := toml.Marshal(r)
	if err != nil {
		return fmt.Errorf("identity: encode receipt: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("identity: mkdir receipts: %w", err)
	}
	return atomicWrite(filepath.Join(dir, r.ID+".toml"), b)
}

// ReadReceipt reads and decodes dir/<id>.toml.
func ReadReceipt(dir, id string) (Receipt, error) {
	b, err := os.ReadFile(filepath.Join(dir, id+".toml"))
	if err != nil {
		return Receipt{}, err
	}
	var r Receipt
	if err := toml.Unmarshal(b, &r); err != nil {
		return Receipt{}, fmt.Errorf("identity: decode receipt: %w", err)
	}
	return r, nil
}

// ListReceipts reads every *.toml receipt in dir (a missing dir yields none).
func ListReceipts(dir string) ([]Receipt, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Receipt
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".toml")
		r, err := ReadReceipt(dir, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// atomicWrite writes b to path via temp + fsync + rename + dir fsync, mirroring
// catalog.WriteAtomic's discipline.
func atomicWrite(path string, b []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".receipt-*.tmp")
	if err != nil {
		return fmt.Errorf("identity: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("identity: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("identity: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("identity: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("identity: rename: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
