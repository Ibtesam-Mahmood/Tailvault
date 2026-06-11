package update

import (
	"fmt"
	"os"
	"path/filepath"
)

// Removable is one path `tailvault update --uninstall` offers to delete, with a
// human label for the confirmation prompt.
type Removable struct {
	Path  string
	Label string
}

// configDir mirrors internal/locations.Path's parent: $XDG_CONFIG_HOME/tailvault
// else ~/.config/tailvault. Kept local to avoid an import cycle / coupling.
func configDir() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "tailvault"), nil
}

// UninstallTargets lists what an uninstall would remove: the running binary plus
// the two client-side state directories (only those that exist). It never
// includes anything inside a repo (tailvault.toml/.lock, hooks) — those are
// per-repo and the user removes them with the documented git steps — and it
// never touches bytes on a storage node.
func UninstallTargets() ([]Removable, error) {
	var out []Removable

	self, err := SelfPath()
	if err != nil {
		return nil, err
	}
	out = append(out, Removable{Path: self, Label: "binary"})

	if cfg, err := configDir(); err == nil {
		if _, statErr := os.Stat(cfg); statErr == nil {
			out = append(out, Removable{Path: cfg, Label: "node registry (locations.toml)"})
		}
	}
	if st, err := stateDir(); err == nil {
		if _, statErr := os.Stat(st); statErr == nil {
			out = append(out, Removable{Path: st, Label: "pull receipts + federation cache"})
		}
	}
	return out, nil
}

// Remove deletes a target. Directories are removed recursively; the binary is a
// single file. Errors are returned so the caller can report a partial uninstall.
func Remove(r Removable) error {
	if err := os.RemoveAll(r.Path); err != nil {
		return fmt.Errorf("remove %s (%s): %w", r.Path, r.Label, err)
	}
	return nil
}
