package cli

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/update"
	"github.com/Ibtesam-Mahmood/tailvault/internal/version"
)

// updateFetcher is the seam for release discovery in `update`. It defaults to the
// real GitHub-backed client and is overridden in tests so the command runs with
// no network (per the no-real-network test rule).
var updateFetcher = func() update.Fetcher { return update.NewClient() }

// newUpdateClient returns the concrete client used for downloads/pinning. Tests
// override updateClient instead.
var updateClient = func() *update.Client { return update.NewClient() }

func newUpdateCmd() *cobra.Command {
	var (
		checkOnly bool
		uninstall bool
		pin       string
		assumeYes bool
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update tailvault in place, check for a newer release, or uninstall",
		Long: "Update tailvault to the latest GitHub release (or a pinned --version), " +
			"verifying the download against the release checksums before replacing the " +
			"running binary.\n\n" +
			"  tailvault update              upgrade to the latest release\n" +
			"  tailvault update --check      report whether a newer release exists\n" +
			"  tailvault update --version vX.Y.Z   install a specific release (pin/downgrade)\n" +
			"  tailvault update --uninstall  remove the binary and client-side state\n\n" +
			"For a private repo, set GITHUB_TOKEN (or GH_TOKEN). Homebrew users should " +
			"prefer `brew upgrade tailvault`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if uninstall {
				return runUninstall(cmd, assumeYes)
			}
			if checkOnly {
				return runUpdateCheck(cmd)
			}
			return runUpdateApply(cmd, pin, assumeYes)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&checkOnly, "check", false, "only report whether a newer release exists; do not modify anything")
	f.BoolVar(&uninstall, "uninstall", false, "remove the tailvault binary and client-side state")
	f.StringVar(&pin, "version", "", "install a specific release tag (e.g. v0.0.106) instead of latest")
	f.BoolVarP(&assumeYes, "yes", "y", false, "do not prompt for confirmation (for non-interactive use)")
	return cmd
}

func runUpdateCheck(cmd *cobra.Command) error {
	res, err := update.Check(cmd.Context(), updateFetcher(), version.Version)
	if err != nil {
		return fmt.Errorf("update check failed: %w", err)
	}
	out := cmd.OutOrStdout()
	switch {
	case res.Available:
		fmt.Fprintf(out, "tailvault: current %s, latest %s — run `tailvault update` to upgrade\n", res.Current, res.Latest)
	case version.Version == "dev":
		fmt.Fprintf(out, "tailvault: this is a dev build; latest release is %s\n", res.Latest)
	default:
		fmt.Fprintf(out, "tailvault: %s is the latest release\n", res.Current)
	}
	return nil
}

func runUpdateApply(cmd *cobra.Command, pin string, assumeYes bool) error {
	out := cmd.OutOrStdout()
	cl := updateClient()

	var (
		rel update.Release
		err error
	)
	if pin != "" {
		rel, err = cl.Tagged(cmd.Context(), pin)
	} else {
		rel, err = cl.Latest(cmd.Context())
	}
	if err != nil {
		return fmt.Errorf("resolve release: %w", err)
	}
	target := strings.TrimPrefix(strings.TrimSpace(rel.Tag), "v")

	if pin == "" && !update.NewerAvailable(version.Version, target) && version.Version != "dev" {
		fmt.Fprintf(out, "tailvault: already on %s (latest is %s); nothing to do\n", version.Version, target)
		return nil
	}

	self, err := update.SelfPath()
	if err != nil {
		return fmt.Errorf("locate running binary: %w", err)
	}

	if !assumeYes {
		ok, err := confirm(cmd, fmt.Sprintf("Update tailvault %s → %s at %s?", version.Version, target, self))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(out, "tailvault: update cancelled")
			return nil
		}
	}

	fmt.Fprintf(out, "tailvault: updating %s → %s\n", version.Version, target)
	if err := cl.Apply(cmd.Context(), rel, target, self); err != nil {
		return fmt.Errorf("apply update: %w", err)
	}
	fmt.Fprintf(out, "tailvault: now on %s\n", target)
	return nil
}

func runUninstall(cmd *cobra.Command, assumeYes bool) error {
	out := cmd.OutOrStdout()
	targets, err := update.UninstallTargets()
	if err != nil {
		return fmt.Errorf("determine uninstall targets: %w", err)
	}
	fmt.Fprintln(out, "tailvault: uninstall will remove:")
	for _, t := range targets {
		fmt.Fprintf(out, "  - %s  (%s)\n", t.Path, t.Label)
	}
	fmt.Fprintln(out, "  (storage-node bytes and per-repo tailvault.toml/.lock are left untouched)")

	if !assumeYes {
		ok, err := confirm(cmd, "Proceed?")
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(out, "tailvault: uninstall cancelled")
			return nil
		}
	}
	var failures int
	for _, t := range targets {
		if err := update.Remove(t); err != nil {
			failures++
			fmt.Fprintf(cmd.ErrOrStderr(), "tailvault: %v\n", err)
			continue
		}
		fmt.Fprintf(out, "tailvault: removed %s\n", t.Label)
	}
	if failures > 0 {
		return fmt.Errorf("uninstall removed %d of %d targets; see errors above", len(targets)-failures, len(targets))
	}
	fmt.Fprintln(out, "tailvault: uninstalled. Thanks for using tailvault.")
	return nil
}

// confirm prompts "<question> [y/N] " on stdout and reads a yes/no from stdin.
// A bare Enter, EOF, or anything other than y/yes is "no".
func confirm(cmd *cobra.Command, question string) (bool, error) {
	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N] ", question)
	r := bufio.NewReader(cmd.InOrStdin())
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return false, nil // EOF with no input → treat as "no"
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// maybeUpdateNotice appends the passive "update available" line to a long-lived
// command's output, best-effort. It never blocks: failures and disabled checks
// produce no output. The fetch is bounded by a short context so a slow/offline
// network cannot hang the host command.
func maybeUpdateNotice(cmd *cobra.Command) {
	if update.NoticeDisabled() {
		return
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
	defer cancel()
	if line := update.NoticeText(ctx, updateFetcher(), version.Version, time.Now); line != "" {
		fmt.Fprintln(cmd.OutOrStdout(), line)
	}
}
