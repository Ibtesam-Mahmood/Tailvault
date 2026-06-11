package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/ingest"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/setup"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// newVaultScanCmd implements ingestion path 1's reconcile (D23): the serverless
// answer to filesystem drift. It diffs disk against the catalog and emits
// catch-up WAL entries for manual adds/edits/moves/deletes.
func newVaultScanCmd() *cobra.Command {
	var dryRun, prune, paranoid bool
	cmd := &cobra.Command{
		Use:   "scan <location>",
		Short: "Reconcile disk against the catalog (absorb manual changes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultScan(cmd, args[0], dryRun, prune, paranoid)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the classified diff and exit (no writes)")
	cmd.Flags().BoolVar(&prune, "prune", false, "apply deletions without per-file confirmation")
	cmd.Flags().BoolVar(&paranoid, "paranoid", false, "hash every entry regardless of mtime/size")
	return cmd
}

func runVaultScan(cmd *cobra.Command, name string, dryRun, prune, paranoid bool) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	reg, err := locations.Load()
	if err != nil {
		return tserr.ConfigErr("load locations.toml", err)
	}
	loc, ok := reg.Locations[name]
	if !ok {
		return tserr.ConfigErr("unknown storage location "+name+" (not in locations.toml)", nil)
	}
	if loc.Backend == locations.BackendSSH {
		return tserr.ConfigErr("vault scan: SSH remote scan is not yet supported; run on a locally-accessible (taildrive/local) root", nil)
	}
	root := loc.BasePath
	catPath := filepath.Join(root, "meta", "catalog.toml")
	cat, err := catalog.Load(catPath)
	if err != nil {
		if os.IsNotExist(err) {
			return tserr.ConfigErr("vault scan: "+name+" is not bootstrapped; run `tailvault vault init "+name+"` first", nil)
		}
		return tserr.ConfigErr("vault scan: load "+catPath, err)
	}

	ig, err := ingest.LoadIgnore(root)
	if err != nil {
		return tserr.ConfigErr("vault scan: read "+ingest.IgnoreFileName, err)
	}
	changes, err := ingest.Diff(ctx, root, ig, cat, paranoid, nil)
	if err != nil {
		return fmt.Errorf("vault scan: %w", err)
	}

	printChanges(out, changes)

	if dryRun {
		return nil
	}

	// Deletes are destructive catalog changes — confirm each unless --prune.
	toApply := changes
	if !prune {
		toApply = confirmDeletes(cmd, changes)
	}

	log := &wal.Log{B: backend.NewFSBackend(root)}
	applied, skipped, err := ingest.Apply(ctx, log, cat, catPath, loc.Node, initActor(cmd), toApply, nil)
	if err != nil {
		return fmt.Errorf("vault scan: %w", err)
	}

	fmt.Fprintf(out, "applied %d change(s); %d skipped\n", len(applied), len(skipped))
	if n := countKind(skipped, ingest.Suspect); n > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: %d suspect file(s) (hash drift, mtime+size unchanged) — run `tailvault verify`; possible corruption\n", n)
	}
	return nil
}

func printChanges(out io.Writer, changes []ingest.Change) {
	counts := map[ingest.ChangeKind]int{}
	for _, c := range changes {
		counts[c.Kind]++
	}
	fmt.Fprintf(out, "scan: %d added, %d edited, %d moved, %d deleted, %d suspect\n",
		counts[ingest.Added], counts[ingest.Edited], counts[ingest.Moved], counts[ingest.Deleted], counts[ingest.Suspect])
	for _, c := range changes {
		switch c.Kind {
		case ingest.Moved:
			fmt.Fprintf(out, "  moved   %s -> %s\n", c.OldPath, c.Path)
		default:
			fmt.Fprintf(out, "  %-7s %s\n", c.Kind, c.Path)
		}
	}
}

// confirmDeletes drops Deleted changes the user does not confirm. Non-delete
// changes pass through untouched.
func confirmDeletes(cmd *cobra.Command, changes []ingest.Change) []ingest.Change {
	pr := setup.NewStdinPrompter(cmd.InOrStdin(), cmd.OutOrStdout())
	out := changes[:0:0]
	for _, c := range changes {
		if c.Kind != ingest.Deleted {
			out = append(out, c)
			continue
		}
		ans, err := pr.AskString(fmt.Sprintf("delete catalog entry %q (file gone from disk)? [y/N]", c.Path), "n")
		if err == nil && isYes(ans) {
			out = append(out, c)
		}
	}
	return out
}

func isYes(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes":
		return true
	}
	return false
}

func countKind(changes []ingest.Change, kind ingest.ChangeKind) int {
	n := 0
	for _, c := range changes {
		if c.Kind == kind {
			n++
		}
	}
	return n
}
