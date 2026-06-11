package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// newVaultStatCmd implements `tailvault vault stat <logical-path | id>`: one
// federated file in full, resolved through the federation. Read-only — no
// password is ever requested (SPEC v2 §16).
func newVaultStatCmd() *cobra.Command {
	var jsonOut, check, long bool
	cmd := &cobra.Command{
		Use:   "stat <logical-path | id>",
		Short: "Show one federated file's metadata and reachability",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultStat(cmd, args[0], statFlags{json: jsonOut, check: check, long: long})
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	cmd.Flags().BoolVar(&check, "check", false, "hash the blob on the home node and report freshness")
	cmd.Flags().BoolVar(&long, "long", false, "show full file IDs and sha256")
	return cmd
}

type statFlags struct{ json, check, long bool }

// statJSON is the scriptable --json contract.
type statJSON struct {
	ID          string   `json:"id"`
	Short       string   `json:"short_id"`
	Path        string   `json:"path"`
	Home        string   `json:"home"`
	SyncMode    string   `json:"sync_mode"`
	Size        int64    `json:"size"`
	SHA256      string   `json:"sha256"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	LastScanned string   `json:"last_scanned,omitempty"`
	Answered    []string `json:"members_answered"`
	Unreachable []string `json:"members_unreachable"`
	MovedHome   bool     `json:"moved_home"`          // found at a non-home member
	Freshness   string   `json:"freshness,omitempty"` // --check result
}

func runVaultStat(cmd *cobra.Command, arg string, fl statFlags) error {
	ctx := cmd.Context()
	reg, err := locations.Load()
	if err != nil {
		return tserr.ConfigErr("load locations.toml", err)
	}
	roster, err := loadRoster(ctx, reg)
	if err != nil {
		return err
	}
	tgt, err := parseTarget(arg)
	if err != nil {
		return err
	}

	var file catalog.File
	var home string
	if tgt.isID {
		file, home, err = fileByIDPrefix(ctx, reg, roster, tgt.id)
	} else {
		file, home, err = fileByPath(ctx, reg, tgt.loc, tgt.rel)
	}
	if err != nil {
		return err
	}

	resolver := &fed.Resolver{
		Roster: roster,
		Q:      fed.NewBackendQuerier(backendForRegistry(reg)),
		Probe:  memberProbe(reg),
	}
	res, err := resolver.Resolve(ctx, file.ID, home)
	if err != nil {
		if errors.Is(err, wal.ErrChainBroken) {
			return tserr.FedChainBrokenErr(home, err) // exit 6
		}
		return err
	}
	warn, oerr := resolveOutcome(res, file.ID)
	if oerr != nil {
		return oerr
	}
	// Prefer the resolver's winning view: it is the authoritative current home.
	if res.View.Found {
		file = res.View.File
		home = res.View.Member
	}

	freshness := ""
	if fl.check {
		freshness = checkFreshness(ctx, reg, roster, home, file)
	}

	out := statJSON{
		ID: file.ID, Short: identity.Short(file.ID), Path: home + "/" + file.Path, Home: home,
		SyncMode: file.SyncMode, Size: file.Size, SHA256: file.SHA256,
		CreatedAt: rfc(file.CreatedAt), UpdatedAt: rfc(file.UpdatedAt), LastScanned: rfc(file.LastScanned),
		Answered: res.Reach.Answered, Unreachable: res.Reach.Unreachable,
		MovedHome: warn, Freshness: freshness,
	}

	if fl.json {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(b))
	} else {
		printStatText(cmd, out, fl.long)
	}
	if warn {
		fmt.Fprintln(cmd.ErrOrStderr(), healWarning(file.ID))
	}
	return nil
}

func printStatText(cmd *cobra.Command, s statJSON, long bool) {
	w := cmd.OutOrStdout()
	id := s.Short
	sha := shortHash(s.SHA256)
	if long {
		id = s.ID
		sha = s.SHA256
	}
	fmt.Fprintf(w, "id         %s\n", id)
	fmt.Fprintf(w, "path       %s\n", s.Path)
	fmt.Fprintf(w, "home       %s\n", s.Home)
	fmt.Fprintf(w, "sync_mode  %s\n", s.SyncMode)
	fmt.Fprintf(w, "size       %s\n", humanBytes(s.Size))
	if s.SyncMode == catalog.SyncModeManual && s.LastScanned != "" {
		fmt.Fprintf(w, "sha256     %s  (as of last scan %s)\n", sha, s.LastScanned)
	} else {
		fmt.Fprintf(w, "sha256     %s\n", sha)
	}
	fmt.Fprintf(w, "created    %s\n", s.CreatedAt)
	fmt.Fprintf(w, "updated    %s\n", s.UpdatedAt)
	if s.Freshness != "" {
		fmt.Fprintf(w, "freshness  %s\n", s.Freshness)
	}
	fmt.Fprintf(w, "members    %d/%d answered", len(s.Answered), len(s.Answered)+len(s.Unreachable))
	if len(s.Unreachable) > 0 {
		fmt.Fprintf(w, " (offline: %v)", s.Unreachable)
	}
	fmt.Fprintln(w)
}

// checkFreshness hashes the blob on the home node and compares to the catalog
// sha. For a manual file a mismatch is "drifted since last scan" (legitimately
// edited in place), NEVER "corrupt" — that verdict belongs to verify (H12). A
// git-mode mismatch is "drifted" too here (stat only reports; verify judges
// corruption against the content-addressed key).
func checkFreshness(ctx context.Context, reg locations.Registry, roster fed.Roster, home string, file catalog.File) string {
	m, ok := roster.Find(home)
	if !ok {
		return "unknown (home not in roster)"
	}
	b, err := backendForRegistry(reg)(m)
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	got, err := b.HashObject(ctx, "objects/"+file.SHA256)
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	if got == file.SHA256 {
		return "fresh"
	}
	return "drifted since last scan"
}

// rfc renders a time as RFC3339 UTC (Z), empty for the zero time.
func rfc(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func shortHash(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
