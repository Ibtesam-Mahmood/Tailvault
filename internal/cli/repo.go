package cli

import (
	"context"
	"fmt"
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

// newTSClient constructs the tailscale client used for command-level preflight /
// identity. It is a package var ONLY so the Block-4 multi-node suite can inject a
// stub (a captured `tailscale status` fixture) and drive preflight-requiring
// commands (gc/push/pull/status) end-to-end without a real tailnet. Production is
// always tailscale.New(); tests restore it via cleanup. Test-only seam, matching
// SetTestGateVerifier/SetTestGCProbe.
var newTSClient = tailscale.New

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
			return "", tserr.ConfigErr("not a tailvault repo: no "+configName+" found in this or any parent directory", nil)
		}
		dir = parent
	}
}

// loadConfig loads and validates tailvault.toml at the command boundary,
// wrapping any parse/validation failure as a TV-CFG-01 config error (exit 2)
// per SPEC §5. Leaf packages keep returning plain errors.
func loadConfig(root string) (*config.Config, error) {
	cfg, err := config.Load(filepath.Join(root, configName))
	if err != nil {
		return nil, tserr.ConfigErr("load "+configName, err)
	}
	return cfg, nil
}

// resolveBackend resolves the repo config's named location into a constructed
// Backend. The SSH backend gets its preflight Ping from the tailscale wrapper.
// The store base is base_path joined with the repo's optional subpath.
func resolveBackend(_ context.Context, cfg *config.Config) (backend.Backend, locations.Location, error) {
	reg, err := locations.Load()
	if err != nil {
		// Wrap a registry read/parse failure as a config error at the boundary
		// (e.g. EACCES on locations.toml would otherwise leak as exit 1).
		return nil, locations.Location{}, tserr.ConfigErr("load locations.toml", err)
	}
	loc, ok := reg.Locations[cfg.Storage.Location]
	if !ok {
		return nil, locations.Location{}, tserr.ConfigErr("unknown storage location "+cfg.Storage.Location+" (not in locations.toml)", nil)
	}
	base := path.Join(loc.BasePath, cfg.Storage.Subpath)
	switch loc.Backend {
	case locations.BackendSSH:
		if loc.User == "" {
			return nil, locations.Location{}, tserr.ConfigErr("ssh location "+cfg.Storage.Location+" missing user", nil)
		}
		return &backend.SSH{
			User:     loc.User,
			Node:     loc.Node,
			BasePath: base,
			Ping:     tailscale.New().Ping,
		}, loc, nil
	case locations.BackendTaildrive:
		// Guard share even though locations.Validate blocks it at Add time — a
		// hand-edited locations.toml could bypass that.
		if loc.Share == "" {
			return nil, locations.Location{}, tserr.ConfigErr("taildrive location "+cfg.Storage.Location+" missing share", nil)
		}
		return backend.NewTaildrive(base), loc, nil
	default:
		return nil, locations.Location{}, tserr.ConfigErr("location "+cfg.Storage.Location+" has unknown backend", nil)
	}
}

// preflightNode is the command-level preflight that runs BEFORE any transfer:
// it confirms the local tailnet session is healthy (TV-NET-01/02) and, per
// backend, that the target is reachable (TV-NODE-01) — an ssh node answers a
// ping; a taildrive share's base_path must exist as a directory.
//
// The taildrive base_path check guards against the hard-fail-violating case
// where an unmounted share's mountpoint is absent: without it, a Put would
// MkdirAll + write to LOCAL disk and falsely report success. (Residual: a
// mountpoint that exists but is unmounted/empty is not detected here — tracked
// as a known v1 limitation; SSH is the hardened MVP path.)
func preflightNode(ctx context.Context, loc locations.Location) error {
	ts := newTSClient()
	if _, err := ts.Status(ctx); err != nil {
		return err // already a typed TV-NET-01/02
	}
	switch loc.Backend {
	case locations.BackendSSH:
		if err := ts.Ping(ctx, loc.Node); err != nil {
			return tserr.NodeOfflineErr(loc.Node, err)
		}
	case locations.BackendTaildrive:
		fi, err := os.Stat(loc.BasePath)
		if err != nil || !fi.IsDir() {
			return tserr.NodeOfflineErr(loc.Node,
				fmt.Errorf("taildrive share base_path %q is not present as a directory (is the share mounted?)", loc.BasePath))
		}
	}
	return nil
}

// whoisSelf resolves the local tailnet identity ("user@host") for the pusher
// stamp; any failure returns an error so the caller falls back to git identity.
func whoisSelf(ctx context.Context) (string, error) {
	ts := newTSClient()
	st, err := ts.Status(ctx)
	if err != nil {
		return "", err
	}
	addr := st.Self.DNSName
	if len(st.Self.IPs) > 0 {
		addr = st.Self.IPs[0]
	}
	if addr == "" {
		return "", tserr.ConfigErr("no self address from tailscale status", nil)
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
