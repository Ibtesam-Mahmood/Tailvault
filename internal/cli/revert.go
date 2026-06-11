package cli

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/config"
	"github.com/Ibtesam-Mahmood/tailvault/internal/gitglue"
	"github.com/Ibtesam-Mahmood/tailvault/internal/revert"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

func newRevertCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revert <path> <sha>",
		Short: "Repoint a history-on file to an older stored version",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, sha := args[0], args[1]
			root, err := findRepoRoot()
			if err != nil {
				return err
			}
			cfg, err := config.Load(filepath.Join(root, configName))
			if err != nil {
				return tserr.ConfigErr("revert: load "+configName, err)
			}
			be, loc, err := resolveBackend(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			if err := preflightNode(cmd.Context(), loc); err != nil {
				return err
			}

			err = revert.Run(cmd.Context(), revert.Options{
				RepoRoot: root,
				Path:     path,
				SHA:      sha,
				Backend:  be,
			})
			switch {
			case errors.Is(err, revert.ErrHistoryOff),
				errors.Is(err, revert.ErrUnknownVersion),
				errors.Is(err, revert.ErrUnknownPath):
				// Precondition failures map to config bucket (exit 2).
				return tserr.ConfigErr(err.Error(), err)
			case err != nil:
				return err // TV-OBJ-01 (missing/corrupt blob) etc. pass through
			}

			// Stage the rewritten lock so the revert is captured on the next commit.
			if err := gitglue.AddPath(root, lockName); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"reverted %s to %s; staged %s — commit to persist\n", path, shortSHA(sha), lockName)
			return nil
		},
	}
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
