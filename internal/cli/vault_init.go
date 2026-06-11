package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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

// newVaultInitCmd implements ingestion path 2 (D18): the first broadcast of a
// storage location into the federation. It tracks every file by default; the
// only opt-outs are .tailvaultignore and --select.
func newVaultInitCmd() *cobra.Command {
	var dryRun, doSelect bool
	cmd := &cobra.Command{
		Use:   "init <location>",
		Short: "Bootstrap a storage location (track all files by default; sync_mode=manual)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultInit(cmd, args[0], dryRun, doSelect)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the ingestion plan and exit (no writes)")
	cmd.Flags().BoolVar(&doSelect, "select", false, "interactively deselect files before ingesting")
	return cmd
}

func runVaultInit(cmd *cobra.Command, name string, dryRun, doSelect bool) error {
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
	// Bootstrap walks + hashes the storage root locally. SSH bootstrap (remote
	// walk over the wire) is not yet supported (DG-33.1) — run on a
	// locally-accessible (taildrive/local) root.
	if loc.Backend == locations.BackendSSH {
		return tserr.ConfigErr("vault init: SSH remote bootstrap is not yet supported; run on a locally-accessible (taildrive/local) root", nil)
	}
	root := loc.BasePath
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return tserr.ConfigErr("vault init: storage root "+root+" is not an accessible directory", err)
	}

	ig, err := ingest.LoadIgnore(root)
	if err != nil {
		return tserr.ConfigErr("vault init: read "+ingest.IgnoreFileName, err)
	}
	plan, err := ingest.BuildPlan(root, ig, nil)
	if err != nil {
		return tserr.ConfigErr("vault init: scan "+root, err)
	}

	if dryRun {
		fmt.Fprintf(out, "plan: %d files to ingest, %d ignored (root %s)\n", len(plan.Files), len(plan.Ignored), root)
		printSample(out, "ignored", plan.Ignored)
		return nil
	}

	if doSelect {
		plan, err = interactiveDeselect(cmd, plan)
		if err != nil {
			return err
		}
	}

	metaDir := filepath.Join(root, "meta")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return tserr.ConfigErr("vault init: create "+metaDir, err)
	}
	catPath := filepath.Join(metaDir, "catalog.toml")

	cat, err := loadOrNewCatalog(catPath, name, loc.Node)
	if err != nil {
		return err
	}

	log := &wal.Log{B: backend.NewFSBackend(root)}
	actor := initActor(cmd)

	before := len(plan.Files)
	progress := func(done, total int, doneBytes, totalBytes int64, current string) {
		fmt.Fprintf(cmd.ErrOrStderr(), "\ringesting %d/%d files (%s / %s) %s",
			done, total, humanBytes(doneBytes), humanBytes(totalBytes), current)
	}
	if err := ingest.Bootstrap(ctx, ingest.BootstrapOpts{
		Root: root, Node: loc.Node, Actor: actor,
		Log: log, Cat: cat, CatPath: catPath, Plan: plan, Progress: progress,
	}); err != nil {
		if before > 0 {
			fmt.Fprintln(cmd.ErrOrStderr())
		}
		// NOTE: a wal.ErrChainBroken should map to TV-FED-03 (exit 6) at this
		// boundary; that tserr constructor is introduced by task-32 (TV-FED
		// codes) and will be wired in on integration. Until then it surfaces as
		// the plain wal error (exit 1). See handoff/EDGE-CASES.
		return fmt.Errorf("vault init: %w", err)
	}
	if before > 0 {
		fmt.Fprintln(cmd.ErrOrStderr())
	}
	fmt.Fprintf(out, "bootstrapped %q: %d files tracked (sync_mode=manual)\n", name, len(plan.Files))
	return nil
}

func loadOrNewCatalog(catPath, vaultName, node string) (*catalog.Catalog, error) {
	cat, err := catalog.Load(catPath)
	if err == nil {
		return cat, nil
	}
	if !os.IsNotExist(err) {
		return nil, tserr.ConfigErr("vault init: load "+catPath, err)
	}
	return &catalog.Catalog{Version: catalog.SchemaVersion, VaultName: vaultName, Node: node}, nil
}

func initActor(cmd *cobra.Command) string {
	if id, err := whoisSelf(cmd.Context()); err == nil && id != "" {
		return id
	}
	if e := gitEmail(); e != "" {
		return e
	}
	return "unknown"
}

func interactiveDeselect(cmd *cobra.Command, plan ingest.Plan) (ingest.Plan, error) {
	pr := setup.NewStdinPrompter(cmd.InOrStdin(), cmd.OutOrStdout())
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "candidate files:")
	for i, c := range plan.Files {
		fmt.Fprintf(out, "  [%d] %s (%s)\n", i, c.Rel, humanBytes(c.Size))
	}
	ans, err := pr.AskString("indices to DESELECT (comma-separated, blank = keep all)", "")
	if err != nil {
		return ingest.Plan{}, err
	}
	drop := map[int]bool{}
	for _, tok := range strings.Split(ans, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		idx, err := strconv.Atoi(tok)
		if err != nil || idx < 0 || idx >= len(plan.Files) {
			return ingest.Plan{}, tserr.ConfigErr("vault init: invalid selection "+tok, nil)
		}
		drop[idx] = true
	}
	kept := plan.Files[:0:0]
	for i, c := range plan.Files {
		if !drop[i] {
			kept = append(kept, c)
		}
	}
	plan.Files = kept
	return plan, nil
}

func printSample(out io.Writer, label string, items []string) {
	const max = 10
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(out, "%s (showing up to %d):\n", label, max)
	for i, s := range items {
		if i >= max {
			fmt.Fprintf(out, "  … and %d more\n", len(items)-max)
			break
		}
		fmt.Fprintf(out, "  %s\n", s)
	}
}

// humanBytes renders a byte count in binary units (SPEC §7).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
