package setup

import (
	"os"
	"path/filepath"

	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
)

// Default suggestions for the interactive flow.
const (
	DefaultBasePath = "/mnt/ssd/tailvault" // nudges users off the boot SD card
	DefaultBackend  = string(locations.BackendSSH)
)

// Prompter abstracts the interactive sequence so BuildLocation stays
// library-agnostic and tests can inject scripted answers.
type Prompter interface {
	SelectPeer(peers []Peer) (Peer, error) // pick-list; only when discovery is viable
	AskString(label, def string) (string, error)
	AskBackend() (string, error) // "ssh" | "taildrive"
}

// DefaultLocalStore returns the default per-name local store path,
// ~/.tailvault/stores/<name>. It is deliberately OUTSIDE any repo so the
// content-addressed blobs never enter git (the whole point of tailvault). When
// the home dir can't be resolved it falls back to a relative path.
func DefaultLocalStore(name string) string {
	if name == "" {
		name = "home"
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".tailvault", "stores", name)
	}
	return filepath.Join(home, ".tailvault", "stores", name)
}

// BuildLocalLocation builds a local-backend Location, prompting only for the
// store path (defaulting to the per-name home store). A local store has no node,
// user, or share — it is a content-addressed directory on this machine.
func BuildLocalLocation(p Prompter, name string) (locations.Location, error) {
	path, err := p.AskString("local store path", DefaultLocalStore(name))
	if err != nil {
		return locations.Location{}, err
	}
	return locations.Location{Backend: locations.BackendLocal, BasePath: path}, nil
}

// BuildLocation runs the registration flow and returns a locations.Location
// ready to persist. A non-empty node skips the pick-list (manual / --node). The
// chosen backend drives whether user (ssh) or share (taildrive) is asked.
//
// It performs no direct I/O of its own (all input comes through p), so it is
// deterministic given scripted answers.
func BuildLocation(p Prompter, peers []Peer, node string) (locations.Location, error) {
	var loc locations.Location

	// Resolve the node: explicit --node/manual wins; otherwise pick from the list.
	if node != "" {
		loc.Node = node
	} else if len(peers) > 0 {
		sel, err := p.SelectPeer(peers)
		if err != nil {
			return loc, err
		}
		loc.Node = sel.Name
	} else {
		n, err := p.AskString("node (MagicDNS name or 100.x IP)", "")
		if err != nil {
			return loc, err
		}
		loc.Node = n
	}

	basePath, err := p.AskString("base_path", DefaultBasePath)
	if err != nil {
		return loc, err
	}
	loc.BasePath = basePath

	be, err := p.AskBackend()
	if err != nil {
		return loc, err
	}
	loc.Backend = locations.Backend(be)

	switch loc.Backend {
	case locations.BackendTaildrive:
		share, err := p.AskString("share", "")
		if err != nil {
			return loc, err
		}
		loc.Share = share
	default: // ssh (and any other) asks for the ssh user
		user, err := p.AskString("user", os.Getenv("USER"))
		if err != nil {
			return loc, err
		}
		loc.User = user
	}

	return loc, nil
}
