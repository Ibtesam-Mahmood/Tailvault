package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/lock"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// entryResolver is the slice of the resolution engine heal needs (satisfied by
// *fed.Resolver) — an interface so the heal core is unit-testable with a stub.
type entryResolver interface {
	Resolve(ctx context.Context, id, homeHint string) (fed.Result, error)
}

// newHealCmd implements `tailvault heal [--dry-run]`: the explicit, never-
// automatic lock-repair command. It resolves every federated lock entry through
// the resolution engine and rewrites the recorded `location` of entries whose
// home has moved, leaving ids/genesis/sha untouched (identity is immutable). It
// is repo-side ONLY — it edits the committed tailvault.lock, which the user then
// commits and pushes; it never writes a WAL or mutates a node.
func newHealCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "heal",
		Short: "Repoint stale tailvault.lock locations from live federation resolution",
		Long: `Resolve every federated lock entry and rewrite the recorded location of any
file whose home has moved, so a later pull fetches from the right member.

heal is repo-side only: it edits the committed tailvault.lock (which you then
commit and push). It never mutates a node or writes a WAL, and it never changes
a file's id, genesis, or sha256 — identity is immutable; only location/path are
repointed.

Entries that resolve to a partial view (some member unreachable) are left
untouched and reported — heal never guesses a location under incomplete answers.
A genuinely missing blob is reported as a verify/repush candidate.

Exit: 0 when every entry is at home or was healed; 6 if any entry remained a
partial view; 5 if any blob was missing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHeal(cmd, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the lock changes without writing")
	return cmd
}

// healChange records one entry's resolution outcome for the report.
type healChange struct {
	path string
	id   string
	from string
	to   string // new location (FoundElsewhere)
}

func runHeal(cmd *cobra.Command, dryRun bool) error {
	ctx := cmd.Context()
	root, err := findRepoRoot()
	if err != nil {
		return err
	}
	lockPath := filepath.Join(root, lockName)
	lk, err := loadLockOrEmpty(lockPath) // self-certifies the committed lock
	if err != nil {
		return err
	}
	reg, err := locations.Load()
	if err != nil {
		return tserr.ConfigErr("load locations.toml", err)
	}
	roster, err := loadRoster(ctx, reg)
	if err != nil {
		return err // no federation → nothing to heal (config error, exit 2)
	}
	resolver := buildResolver(reg, roster)

	healed, partial, missing, atHome, changed, err := healLock(ctx, lk, resolver, dryRun)
	if err != nil {
		return err
	}

	// Write the canonical lock once, after all entries are resolved, unless dry-run.
	// Healed entries (location repointed) are persisted even when other entries
	// remain partial/missing — those are left exactly as they were.
	if changed && !dryRun {
		if err := lk.Validate(); err != nil {
			return tserr.ConfigErr("heal produced an invalid lock", err)
		}
		if err := lock.Write(lockPath, lk, "tailvault heal"); err != nil {
			return err
		}
	}

	printHealReport(cmd, healed, partial, missing, atHome, dryRun)

	// Exit code reflects unresolved state in BOTH modes (the partial/missing state
	// exists regardless of whether we wrote): partial view (6) outranks missing (5).
	if len(partial) > 0 {
		return tserr.FedPartialViewErr(identity.Short(partial[0].id), nil, nil)
	}
	if len(missing) > 0 {
		return tserr.ObjMissingErr(identity.Short(missing[0].id), nil)
	}
	return nil
}

// healLock resolves every federated entry and (unless dryRun) repoints the
// location of FoundElsewhere entries in place. It NEVER touches id/genesis/sha
// — identity is immutable; only location is rewritten. PartialView/Missing
// entries are left exactly as they were and bucketed for reporting. A
// chain-broken member surfaces as a TV-FED-03 error (exit 6).
func healLock(ctx context.Context, lk *lock.Lock, r entryResolver, dryRun bool) (healed, partial, missing, atHome []healChange, changed bool, err error) {
	for i := range lk.Entries {
		e := &lk.Entries[i]
		if e.ID == "" {
			continue // non-federated entry — heal skips it
		}
		res, rerr := r.Resolve(ctx, e.ID, e.Location)
		if rerr != nil {
			if errors.Is(rerr, wal.ErrChainBroken) {
				return nil, nil, nil, nil, false, tserr.FedChainBrokenErr(e.Location, rerr) // exit 6
			}
			return nil, nil, nil, nil, false, rerr
		}
		ch := healChange{path: e.Path, id: e.ID, from: e.Location}
		switch res.Outcome {
		case fed.FoundAtHome:
			atHome = append(atHome, ch)
		case fed.FoundElsewhere:
			ch.to = res.View.Member
			healed = append(healed, ch)
			if !dryRun {
				e.Location = res.View.Member // repoint location only; identity untouched
			}
			changed = true
		case fed.PartialView:
			partial = append(partial, ch)
		case fed.Missing:
			missing = append(missing, ch)
		default:
			return nil, nil, nil, nil, false, fmt.Errorf("heal: unexpected resolution outcome %s for %s", res.Outcome, identity.Short(e.ID))
		}
	}
	return healed, partial, missing, atHome, changed, nil
}

func printHealReport(cmd *cobra.Command, healed, partial, missing, atHome []healChange, dryRun bool) {
	w := cmd.OutOrStdout()
	verb := "healed"
	if dryRun {
		verb = "would heal"
	}
	for _, c := range healed {
		fmt.Fprintf(w, "%s %s (id %s): %s -> %s\n", verb, c.path, identity.Short(c.id), c.from, c.to)
	}
	ew := cmd.ErrOrStderr()
	for _, c := range partial {
		fmt.Fprintf(ew, "warning: %s (id %s): partial view — cannot heal (a member is unreachable)\n", c.path, identity.Short(c.id))
	}
	for _, c := range missing {
		fmt.Fprintf(ew, "warning: %s (id %s): blob missing — run `tailvault verify` or repush\n", c.path, identity.Short(c.id))
	}
	fmt.Fprintf(w, "%s %d, at home %d, partial %d, missing %d\n", verb, len(healed), len(atHome), len(partial), len(missing))
}
