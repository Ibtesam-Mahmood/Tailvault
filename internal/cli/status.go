package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/status"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

func newStatusCmd() *cobra.Command {
	var checkBlobs bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show local-only / pushed / drifted / orphaned files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := findRepoRoot()
			if err != nil {
				return err
			}
			cfg, err := loadConfig(root)
			if err != nil {
				return err
			}
			lk, err := loadLockOrEmpty(filepath.Join(root, lockName))
			if err != nil {
				return err
			}
			managed, err := status.ManagedFiles(cfg, root)
			if err != nil {
				return err
			}
			treeSHA, err := status.ScanTree(root, managed)
			if err != nil {
				return err
			}

			var present map[string]bool
			if checkBlobs {
				b, _, berr := resolveBackend(cmd.Context(), cfg)
				if berr != nil {
					return berr
				}
				present, err = statBlobs(cmd.Context(), b, treeSHA, status.ByPath(lk))
				if err != nil {
					return err
				}
			}

			rows := status.Classify(treeSHA, status.ByPath(lk), present)
			return printStatus(cmd, rows)
		},
	}
	cmd.Flags().BoolVar(&checkBlobs, "check-blobs", false, "contact the node to confirm each pushed blob is present")
	return cmd
}

// loadLockOrEmpty loads the lock, treating a missing file as an empty lock so
// status/push work before the first push. A parse failure is wrapped as a
// TV-CFG-01 config error (exit 2) at the command boundary per SPEC §5.
func loadLockOrEmpty(path string) (*lock.Lock, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return &lock.Lock{Version: lock.SchemaVersion}, nil
	}
	lk, err := lock.Load(path)
	if err != nil {
		return nil, tserr.ConfigErr("load "+lockName, err)
	}
	// Self-certify the committed lock: a v1 lock (D29) or a torn id↔genesis
	// pairing is a hard config failure (exit 2), never silently trusted.
	if err := lk.Validate(); err != nil {
		return nil, tserr.ConfigErr("validate "+lockName, err)
	}
	return lk, nil
}

// statBlobs Stats objects/<sha> for each pushed candidate (tree sha == lock sha)
// and returns sha -> present.
func statBlobs(ctx context.Context, b backend.Backend, treeSHA map[string]string, locked map[string]lock.Entry) (map[string]bool, error) {
	present := map[string]bool{}
	for path, sha := range treeSHA {
		e, ok := locked[path]
		if !ok || e.SHA256 != sha {
			continue
		}
		if _, seen := present[sha]; seen {
			continue
		}
		m, err := b.Stat(ctx, "objects/"+sha)
		if err != nil {
			return nil, err
		}
		present[sha] = m.Exists
	}
	return present, nil
}

func printStatus(cmd *cobra.Command, rows []status.Row) error {
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATE\tPATH\tSHA")
	for _, r := range rows {
		sha := r.SHA
		if len(sha) > 8 {
			sha = sha[:8]
		}
		note := ""
		if r.BlobMissing {
			note = "  (blob missing!)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s%s\n", r.State, r.Path, sha, note)
	}
	return tw.Flush()
}
