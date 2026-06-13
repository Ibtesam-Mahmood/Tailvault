// Package appconfig owns tailvault's user-level, machine-wide settings at
// ~/.config/tailvault/config.toml (honoring XDG_CONFIG_HOME) — distinct from the
// per-target locations registry (internal/locations) and from any per-repo file.
//
// Today it carries a single field: the resolved path to the `tailscale` CLI, so
// machines where Tailscale ships as a GUI app (its binary not on PATH — common
// on macOS and Windows) can record where to find it once (via `tailvault config`)
// instead of failing peer discovery on every run. The package is a leaf: it
// imports only the stdlib + the TOML codec, so internal/tailscale may depend on
// it without an import cycle.
package appconfig

import (
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the whole config.toml document. Fields are omitempty so an unset
// value never writes a noisy empty key.
type Config struct {
	// TailscalePath is an absolute path to the tailscale CLI binary. Empty means
	// "resolve normally" (PATH + well-known locations). Set by `tailvault config`.
	TailscalePath string `toml:"tailscale_path,omitempty"`
}

// Path returns the config path, honoring XDG_CONFIG_HOME, defaulting to
// ~/.config/tailvault/config.toml. It mirrors locations.Path so both files live
// side by side under the same tailvault config dir.
func Path() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "tailvault", "config.toml"), nil
}

// Load reads the config. A missing file yields a zero Config (not an error) so a
// fresh machine behaves exactly like one with an empty config.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var c Config
	if err := toml.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Save writes the config, creating ~/.config/tailvault/ (0700) if needed and
// writing the file 0600 (it records a local filesystem path).
func (c Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// TailscalePath is a convenience reader: the configured tailscale binary path,
// or "" if unset or unreadable. It never errors — callers treat "" as "fall back
// to normal resolution", so a malformed config degrades to default behavior
// rather than breaking every command.
func TailscalePath() string {
	c, err := Load()
	if err != nil {
		return ""
	}
	return c.TailscalePath
}
