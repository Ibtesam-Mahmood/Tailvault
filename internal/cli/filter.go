package cli

import (
	"errors"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/filter"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// newFilterCleanCmd is the hidden `tailvault filter-clean %f` git invokes on
// staging. It needs no backend or node: it only hashes the content and emits the
// pointer (push uploads the blob later by re-scanning the tree). Keeping it
// node-free means `git add` works on a repo whose location is not yet reachable.
func newFilterCleanCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "filter-clean <path>",
		Short:  "Internal git clean filter (real bytes -> pointer)",
		Hidden: true,
		Args:   cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadRepoConfig()
			if err != nil {
				return err
			}
			path := ""
			if len(args) > 0 {
				path = args[0]
			}
			env := &filter.Env{Cfg: cfg, Location: cfg.Storage.Location}
			return mapFilterErr(filter.Clean(cmd.Context(), env, path, cmd.InOrStdin(), cmd.OutOrStdout()))
		},
	}
}

// newFilterSmudgeCmd is the hidden `tailvault filter-smudge %f` git invokes on
// checkout. It resolves the backend to fetch the real bytes for a pointer.
func newFilterSmudgeCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "filter-smudge <path>",
		Short:  "Internal git smudge filter (pointer -> real bytes)",
		Hidden: true,
		Args:   cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadRepoConfig()
			if err != nil {
				return err
			}
			be, _, err := resolveBackend(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			env := &filter.Env{Cfg: cfg, Backend: be, Location: cfg.Storage.Location}
			return mapFilterErr(filter.Smudge(cmd.Context(), env, cmd.InOrStdin(), cmd.OutOrStdout()))
		},
	}
}

// loadRepoConfig finds the repo root and loads tailvault.toml, wrapping a
// load/parse failure as a config error (exit 2).
func loadRepoConfig() (*config.Config, error) {
	root, err := findRepoRoot()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(filepath.Join(root, configName))
	if err != nil {
		return nil, tserr.ConfigErr("filter: load "+configName, err)
	}
	return cfg, nil
}

// mapFilterErr routes filter errors to the right exit bucket: an already-typed
// tserr.Error (a missing/corrupt blob = TV-OBJ-01, exit 5) passes through; any
// other failure (a malformed pointer, a bad rule glob, an I/O error) is a
// config/precondition failure (TV-CFG-01, exit 2) so git aborts legibly.
func mapFilterErr(err error) error {
	if err == nil {
		return nil
	}
	var te *tserr.Error
	if errors.As(err, &te) {
		return err
	}
	return tserr.ConfigErr(err.Error(), err)
}
