package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/gitglue"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/setup"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// registerLocal runs the default `tailvault setup` flow: create a LOCAL storage
// location on this machine. Interactive (no --path): confirm intent, prompt for a
// name (defaulting to the repo folder, else "home"), choose where the store lives
// (current folder / home / another path), then confirm. Scriptable: --name +
// --path skip every prompt. In all cases a store path INSIDE a git working tree
// is refused (blobs would pollute the repo).
func registerLocal(cmd *cobra.Command, name, pathFlag string) error {
	pr := setup.NewStdinPrompter(cmd.InOrStdin(), cmd.OutOrStdout())
	out := cmd.OutOrStdout()
	interactive := pathFlag == ""

	if !interactive && name == "" {
		return tserr.ConfigErr("setup: --path requires --name", nil)
	}

	if name == "" {
		def := "home"
		if root, err := gitglue.RepoRoot(""); err == nil && root != "" {
			def = filepath.Base(root) // decision: derive the name from the repo folder
		}
		n, err := pr.AskString("location name", def)
		if err != nil {
			return err
		}
		if n == "" {
			return fmt.Errorf("setup: location name is required")
		}
		name = n
	}

	var path string
	if interactive {
		p, err := chooseLocalPath(pr, name)
		if err != nil {
			return err
		}
		path = p
	} else {
		path = pathFlag
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}

	// Guard: never create a content-addressed store inside a git working tree.
	if err := guardLocalStorePath(name, path); err != nil {
		return err
	}

	if interactive && !askYesNo(pr, fmt.Sprintf("Create local store %q at %s?", name, path), true) {
		fmt.Fprintln(out, "aborted")
		return nil
	}

	loc := locations.Location{Backend: locations.BackendLocal, BasePath: path}
	reg, err := locations.Load()
	if err != nil {
		return err
	}
	if err := reg.Add(name, loc); err != nil {
		return err
	}
	if err := reg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(out, "registered local location %q (base_path %s)\n", name, path)
	return nil
}

// askYesNo reads a y/n answer, returning def on an empty line.
func askYesNo(pr *setup.StdinPrompter, q string, def bool) bool {
	d := "n"
	if def {
		d = "y"
	}
	ans, err := pr.AskString(q+" [y/n]", d)
	if err != nil {
		return false
	}
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

// chooseLocalPath presents the store-location menu and returns the chosen path.
// The home option is offered only when that path does not already exist ("only
// if not already set"); the current folder is always offered; "o" / any other
// text takes a free path.
func chooseLocalPath(pr *setup.StdinPrompter, name string) (string, error) {
	cwd, _ := os.Getwd()
	homeDef := setup.DefaultLocalStore(name)
	_, homeErr := os.Stat(homeDef)
	homeAvailable := os.IsNotExist(homeErr)

	fmt.Fprintln(pr.Out, "where should the store live?")
	if homeAvailable {
		fmt.Fprintf(pr.Out, "  h) home            %s\n", homeDef)
	} else {
		fmt.Fprintf(pr.Out, "  (home %s already exists — pick another)\n", homeDef)
	}
	fmt.Fprintf(pr.Out, "  c) current folder  %s\n", cwd)
	fmt.Fprintln(pr.Out, "  o) other path")

	def := "h"
	if !homeAvailable {
		def = "c"
	}
	choice, err := pr.AskString("choice", def)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "h":
		return homeDef, nil
	case "c":
		return cwd, nil
	case "o":
		return pr.AskString("store path", "")
	default:
		return choice, nil // treat anything else as a literal path
	}
}

// guardLocalStorePath refuses a local store whose path falls inside a git working
// tree (the store root or any path within a repo) — content-addressed blobs there
// would be tracked by git, defeating the point of tailvault. Home and any path
// outside a repo pass.
func guardLocalStorePath(name, path string) error {
	if root, inside := storePathInGitRepo(path); inside {
		return tserr.ConfigErr(fmt.Sprintf(
			"refusing to create a local store at %s: it is inside the git repo %s — blobs would pollute the repo. Use ~/.tailvault/stores/%s or a path outside any repo.",
			path, root, name), nil)
	}
	return nil
}

// storePathInGitRepo reports the git repo root if path lies inside a git working
// tree. For a not-yet-created path it checks the nearest existing ancestor, so a
// planned subfolder of a repo is still caught.
func storePathInGitRepo(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	d := abs
	for {
		if fi, statErr := os.Stat(d); statErr == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
	root, err := gitglue.RepoRoot(d)
	if err != nil || root == "" {
		return "", false
	}
	return root, true
}

// statusForDiscovery is the seam for tailnet peer discovery in the interactive
// flow. It defaults to the real local session but is overridden in tests so the
// flow runs deterministically with no real tailscale daemon (per the
// no-real-Tailscale test rule).
var statusForDiscovery = func(ctx context.Context) (tailscale.Status, error) {
	return tailscale.New().Status(ctx)
}

// registerInteractive runs the interactive node-registration flow shared by
// `setup` and the interactive form of `location add`: it enumerates online
// peers from the local Tailscale session (unless node is given), prompts for the
// remaining fields, and persists the entry via the locations registry.
//
// Discovery failure is non-fatal — it prints one stderr line and falls back to
// manual entry. The flow never performs a Tailscale login or API call.
func registerInteractive(cmd *cobra.Command, name, node string) error {
	pr := setup.NewStdinPrompter(cmd.InOrStdin(), cmd.OutOrStdout())

	if name == "" {
		n, err := pr.AskString("location name", "")
		if err != nil {
			return err
		}
		if n == "" {
			return fmt.Errorf("setup: location name is required")
		}
		name = n
	}

	var peers []setup.Peer
	if node == "" {
		st, err := statusForDiscovery(cmd.Context())
		if p, ok := setup.OnlinePeers(st, err); ok {
			peers = p
		} else if _, found := tailscale.Locate(); !found {
			// The binary itself is missing — a failed resolution, not a down
			// daemon. Flag the real fix (PATH / install / register) instead of a
			// generic "unavailable", then fall back to manual entry.
			fmt.Fprintln(cmd.ErrOrStderr(), "tailscale CLI not found on PATH or in any known location — peer auto-detect is off.")
			fmt.Fprintln(cmd.ErrOrStderr(), "  fix: install Tailscale, or run `tailvault config` to locate and register it (or set TAILVAULT_TAILSCALE).")
			fmt.Fprintln(cmd.ErrOrStderr(), "Entering manual mode.")
		} else {
			// Binary resolves but the session isn't usable (daemon down / logged out).
			fmt.Fprintln(cmd.ErrOrStderr(), "Tailscale peer discovery unavailable (daemon down or not logged in); entering manual mode.")
		}
	}

	loc, err := setup.BuildLocation(pr, peers, node)
	if err != nil {
		return err
	}

	reg, err := locations.Load()
	if err != nil {
		return err
	}
	if err := reg.Add(name, loc); err != nil {
		return err
	}
	if err := reg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "registered location %q (%s on %s)\n", name, loc.Backend, loc.Node)
	return nil
}
