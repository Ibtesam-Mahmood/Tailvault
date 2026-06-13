package tailscale

import (
	"os"
	"os/exec"
	"runtime"

	"github.com/Ibtesam-Mahmood/tailvault/internal/appconfig"
)

// binName is the tailscale CLI's executable name for the current OS.
func binName() string {
	if runtime.GOOS == "windows" {
		return "tailscale.exe"
	}
	return "tailscale"
}

// wellKnownPaths lists OS-specific absolute locations the tailscale CLI commonly
// installs to when it is NOT on PATH — notably the GUI app bundles on macOS and
// Windows, where the binary is reachable but the shell PATH never learns about
// it. Resolution falls back to these only after PATH lookup fails.
func wellKnownPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Tailscale.app/Contents/MacOS/Tailscale", // App Store / standalone GUI app
			"/usr/local/bin/tailscale",                             // pkg installer / Intel Homebrew
			"/opt/homebrew/bin/tailscale",                          // Apple-Silicon Homebrew
		}
	case "windows":
		return []string{
			`C:\Program Files\Tailscale\tailscale.exe`,
			`C:\Program Files (x86)\Tailscale\tailscale.exe`,
		}
	default: // linux and friends — usually already on PATH; cover the common dirs
		return []string{
			"/usr/bin/tailscale",
			"/usr/local/bin/tailscale",
		}
	}
}

// candidates is the ordered resolution list: an explicit env override, the
// persisted config path, the PATH name, then the OS well-known locations. Empty
// entries are skipped by Locate.
func candidates() []string {
	return append([]string{
		os.Getenv("TAILVAULT_TAILSCALE"), // highest priority: explicit override
		appconfig.TailscalePath(),        // persisted by `tailvault config`
		binName(),                        // PATH lookup
	}, wellKnownPaths()...)
}

// Locate finds the tailscale CLI binary WITHOUT executing it, trying each
// candidate in order (env override → config → PATH → OS well-known dirs) and
// returning the first that resolves to an executable, with true. It returns
// ("", false) when no binary is found anywhere — the signal the interactive
// setup flow and `tailvault config` use to tell the user to install Tailscale or
// add it to PATH, instead of silently dropping to manual entry.
//
// exec.LookPath handles both a bare name (searched on PATH) and an absolute path
// (validated as an executable), so a single call covers every candidate shape.
func Locate() (string, bool) {
	for _, c := range candidates() {
		if c == "" {
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p, true
		}
	}
	return "", false
}

// resolveBinary returns the binary the default execRunner should invoke. It
// prefers a located path; if nothing resolves it falls back to the bare name so
// the eventual exec fails with exec.ErrNotFound — which Status maps to
// TV-NET-01 (isBinaryMissing) exactly as before this resolver existed.
func resolveBinary() string {
	if p, ok := Locate(); ok {
		return p
	}
	return binName()
}
