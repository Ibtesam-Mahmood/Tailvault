// Package locations owns the user-level storage registry at
// ~/.config/tailvault/locations.toml (honoring XDG_CONFIG_HOME). The registry
// is deliberately kept OUT of the repo — the repo's tailvault.toml references a
// location only by name. This package reads/writes the registry losslessly and
// reports live reachability for `tailvault location ls`.
package locations

import (
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// Backend identifies how a location's bytes are transferred.
type Backend string

const (
	BackendSSH       Backend = "ssh"
	BackendTaildrive Backend = "taildrive"
	BackendLocal     Backend = "local" // content-addressed local filesystem; no node/user/share
)

// Location is a single registered storage target.
type Location struct {
	Node     string  `toml:"node"`
	BasePath string  `toml:"base_path"`
	Backend  Backend `toml:"backend"`
	User     string  `toml:"user,omitempty"`  // ssh
	Share    string  `toml:"share,omitempty"` // taildrive
}

// Registry is the whole locations.toml document: name -> Location.
type Registry struct {
	Locations map[string]Location `toml:"locations"`
}

// Path returns the registry path, honoring XDG_CONFIG_HOME, defaulting to
// ~/.config/tailvault/locations.toml.
func Path() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "tailvault", "locations.toml"), nil
}

// Load reads the registry. A missing file yields an empty Registry (not an
// error) so the first `location add` works on a fresh machine.
func Load() (Registry, error) {
	path, err := Path()
	if err != nil {
		return Registry{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Registry{Locations: map[string]Location{}}, nil
		}
		return Registry{}, err
	}
	var r Registry
	if err := toml.Unmarshal(data, &r); err != nil {
		return Registry{}, tserr.ConfigErr(fmt.Sprintf("parse %s", path), err)
	}
	if r.Locations == nil {
		r.Locations = map[string]Location{}
	}
	return r, nil
}

// Save writes the registry, creating ~/.config/tailvault/ (0700) if needed and
// writing the file 0600 (it names internal infra paths).
func (r Registry) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := toml.Marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Add inserts or updates a named entry after validating required fields per
// backend. ssh requires node, base_path, user; taildrive requires node,
// base_path, share.
func (r *Registry) Add(name string, loc Location) error {
	if name == "" {
		return tserr.ConfigErr("location name must not be empty", nil)
	}
	if err := loc.Validate(); err != nil {
		return err
	}
	if r.Locations == nil {
		r.Locations = map[string]Location{}
	}
	r.Locations[name] = loc
	return nil
}

// Remove deletes a named entry. It errors when the name is absent so callers can
// report a clear "not registered" instead of silently succeeding.
func (r *Registry) Remove(name string) error {
	if _, ok := r.Locations[name]; !ok {
		return tserr.ConfigErr(fmt.Sprintf("location %q is not registered", name), nil)
	}
	delete(r.Locations, name)
	return nil
}

// Validate checks that a Location has the fields its backend requires. Every
// backend needs base_path; ssh/taildrive additionally need a node (and user /
// share), while local needs ONLY base_path and rejects node/user/share so a
// stray value (e.g. a half-edited remote entry) is caught rather than ignored.
func (loc Location) Validate() error {
	if loc.BasePath == "" {
		return tserr.ConfigErr("location: base_path is required", nil)
	}
	switch loc.Backend {
	case BackendLocal:
		if loc.Node != "" || loc.User != "" || loc.Share != "" {
			return tserr.ConfigErr("location: local backend takes only base_path (no node/user/share)", nil)
		}
	case BackendSSH:
		if loc.Node == "" {
			return tserr.ConfigErr("location: node is required", nil)
		}
		if loc.User == "" {
			return tserr.ConfigErr("location: ssh backend requires user", nil)
		}
	case BackendTaildrive:
		if loc.Node == "" {
			return tserr.ConfigErr("location: node is required", nil)
		}
		if loc.Share == "" {
			return tserr.ConfigErr("location: taildrive backend requires share", nil)
		}
	case "":
		return tserr.ConfigErr("location: backend is required (local|ssh|taildrive)", nil)
	default:
		return tserr.ConfigErr(fmt.Sprintf("location: unknown backend %q (want local|ssh|taildrive)", loc.Backend), nil)
	}
	return nil
}
