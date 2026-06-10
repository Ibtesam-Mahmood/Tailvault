package cli

import (
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

const (
	configName = "tailvault.toml"
	lockName   = "tailvault.lock"
)

// cliCfgErr builds a config/precondition error (TV-CFG-01, exit bucket 2).
// TODO(coder-ws-a): replace with tserr.ConfigErr once it lands.
func cliCfgErr(cause string, err error) *tserr.Error {
	return &tserr.Error{Code: tserr.Code("TV-CFG-01"), Cause: cause, Fix: "correct the configuration and retry", Err: err}
}

// findRepoRoot walks up from the current directory to the first directory that
// contains a tailvault.toml, returning that directory.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, configName)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", cliCfgErr("not a tailvault repo: no "+configName+" found in this or any parent directory", nil)
		}
		dir = parent
	}
}

// resolveBackend resolves the repo config's named location into a constructed
// Backend. The SSH backend gets its preflight Ping from the tailscale wrapper.
// The store base is base_path joined with the repo's optional subpath.
func resolveBackend(_ context.Context, cfg *config.Config) (backend.Backend, locations.Location, error) {
	reg, err := locations.Load()
	if err != nil {
		return nil, locations.Location{}, err
	}
	loc, ok := reg.Locations[cfg.Storage.Location]
	if !ok {
		return nil, locations.Location{}, cliCfgErr("unknown storage location "+cfg.Storage.Location+" (not in locations.toml)", nil)
	}
	base := path.Join(loc.BasePath, cfg.Storage.Subpath)
	switch loc.Backend {
	case locations.BackendSSH:
		return &backend.SSH{
			User:     loc.User,
			Node:     loc.Node,
			BasePath: base,
			Ping:     tailscale.New().Ping,
		}, loc, nil
	case locations.BackendTaildrive:
		return backend.NewTaildrive(base), loc, nil
	default:
		return nil, locations.Location{}, cliCfgErr("location "+cfg.Storage.Location+" has unknown backend", nil)
	}
}

// preflightNode is the command-level preflight that runs BEFORE any transfer:
// it confirms the local tailnet session is healthy (TV-NET-01/02) and, for an
// ssh location, that the node answers a ping (TV-NODE-01). Taildrive relies on
// the backend's own os-level errors for an unmounted/unwritable share.
func preflightNode(ctx context.Context, loc locations.Location) error {
	ts := tailscale.New()
	if _, err := ts.Status(ctx); err != nil {
		return err // already a typed TV-NET-01/02
	}
	if loc.Backend == locations.BackendSSH {
		if err := ts.Ping(ctx, loc.Node); err != nil {
			return tserr.NodeOfflineErr(loc.Node, err)
		}
	}
	return nil
}

// whoisSelf resolves the local tailnet identity ("user@host") for the pusher
// stamp; any failure returns an error so the caller falls back to git identity.
func whoisSelf(ctx context.Context) (string, error) {
	ts := tailscale.New()
	st, err := ts.Status(ctx)
	if err != nil {
		return "", err
	}
	addr := st.Self.DNSName
	if len(st.Self.IPs) > 0 {
		addr = st.Self.IPs[0]
	}
	if addr == "" {
		return "", cliCfgErr("no self address from tailscale status", nil)
	}
	return ts.Whois(ctx, addr)
}

// gitEmail returns `git config user.email`, or "" when unavailable.
func gitEmail() string {
	out, err := exec.Command("git", "config", "user.email").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
