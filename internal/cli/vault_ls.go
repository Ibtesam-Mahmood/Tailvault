package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// newVaultLsCmd implements `tailvault vault ls [<location>[/<path>]]`: browse the
// logical federated tree assembled by fanning out to every roster member. With
// no argument it lists members; with a location[/path] it lists entries under
// that logical folder. Read-only — no password (SPEC v2 §16). Per D27 its scope
// is all members; partial views are first-class and clearly marked, never an
// empty listing pretending to be authoritative.
func newVaultLsCmd() *cobra.Command {
	var jsonOut, long, idsOnly bool
	cmd := &cobra.Command{
		Use:   "ls [<location>[/<path>]]",
		Short: "List the federated logical tree (members, or entries under a folder)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := ""
			if len(args) == 1 {
				arg = args[0]
			}
			return runVaultLs(cmd, arg, lsFlags{json: jsonOut, long: long, idsOnly: idsOnly})
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	cmd.Flags().BoolVar(&long, "long", false, "show full file IDs and sha256")
	cmd.Flags().BoolVar(&idsOnly, "ids-only", false, "print only file IDs")
	return cmd
}

type lsFlags struct{ json, long, idsOnly bool }

// memberState is one member's contribution to the fan-out: its reachability and,
// when reachable, the files it reported. Offline members are filled from the
// advisory cache (marked stale) so "was here, now offline" is distinguishable
// from "never existed".
type memberState struct {
	Name      string         `json:"name"`
	Node      string         `json:"node"`
	Online    bool           `json:"online"`
	LastSeen  string         `json:"last_seen,omitempty"` // cache "last seen" for offline members
	Cached    bool           `json:"cached"`              // entries came from the cache, not live
	FileCount int            `json:"file_count"`
	Size      int64          `json:"total_size"`
	files     []catalog.File // live or cached entries (cached carry only id/path)
}

type lsJSON struct {
	Members     []memberState `json:"members"`
	Entries     []entryJSON   `json:"entries,omitempty"`
	Answered    []string      `json:"members_answered"`
	Unreachable []string      `json:"members_unreachable"`
}

type entryJSON struct {
	ID       string `json:"id"`
	Short    string `json:"short_id"`
	Path     string `json:"path"` // logical "<member>/<rel>"
	SyncMode string `json:"sync_mode"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Cached   bool   `json:"cached"`
}

func runVaultLs(cmd *cobra.Command, arg string, fl lsFlags) error {
	ctx := cmd.Context()
	reg, err := locations.Load()
	if err != nil {
		return tserr.ConfigErr("load locations.toml", err)
	}
	roster, err := loadRoster(ctx, reg)
	if err != nil {
		return err
	}

	active := roster.Active()
	reach := fed.Probe(ctx, active, memberProbe(reg))
	answered := map[string]bool{}
	for _, n := range reach.Answered {
		answered[n] = true
	}

	cache := &fed.Cache{Dir: cacheDir(roster.FedID)}
	states := fanOut(ctx, reg, active, answered, cache)
	recordSnapshot(cache, roster.FedID, states)

	// Filter to a location (and optional path prefix) when an arg is given.
	var locFilter, pathFilter string
	if arg != "" {
		locFilter, pathFilter, _ = strings.Cut(arg, "/")
	}

	if arg == "" {
		// Top level: one row per member.
		return emitMembers(cmd, fl, states, reach)
	}

	entries := collectEntries(states, locFilter, pathFilter)
	if len(entries) == 0 && reach.Partial() {
		// Cannot prove the folder is empty while a member is unreachable.
		return tserr.FedPartialViewErr(arg, reach.Unreachable, nil) // exit 6
	}
	return emitEntries(cmd, fl, entries, reach)
}

// fanOut probes-then-reads each member: reachable members contribute live files;
// unreachable members are backfilled from the advisory cache (marked stale).
func fanOut(ctx context.Context, reg locations.Registry, active []catalog.Member, answered map[string]bool, cache *fed.Cache) []memberState {
	bf := backendForRegistry(reg)
	current, previous, _ := cache.Load()
	states := make([]memberState, 0, len(active))
	for _, m := range active {
		st := memberState{Name: m.Name, Node: m.Node, Online: answered[m.Name]}
		if st.Online {
			if b, err := bf(m); err == nil {
				if cat, err := readCatalog(ctx, b); err == nil && cat != nil {
					st.files = cat.Files
					st.FileCount = len(cat.Files)
					for _, f := range cat.Files {
						st.Size += f.Size
					}
				}
			}
		} else if ms := lastKnownSummary(current, previous, m.Name); ms != nil {
			st.Cached = true
			st.LastSeen = rfc(ms.LastSeen)
			st.FileCount = ms.FileCount
			for _, id := range ms.IDs {
				st.files = append(st.files, catalog.File{ID: id})
			}
		}
		states = append(states, st)
	}
	return states
}

// lastKnownSummary finds a member's most recent cached summary (current over
// previous), or nil — the "was here, now offline" advisory signal.
func lastKnownSummary(current, previous *fed.Snapshot, name string) *fed.MemberSummary {
	for _, s := range []*fed.Snapshot{current, previous} {
		if s == nil {
			continue
		}
		for i := range s.Members {
			if s.Members[i].Name == name {
				return &s.Members[i]
			}
		}
	}
	return nil
}

// recordSnapshot persists the live view (reachable members only carry real
// data) so a later offline ls can show "last seen". Advisory; failures are
// ignored (the cache never gates correctness).
func recordSnapshot(cache *fed.Cache, fedID string, states []memberState) {
	snap := fed.Snapshot{FedID: fedID}
	for _, st := range states {
		if !st.Online {
			continue // never overwrite a live member's record with a stale one
		}
		ids := make([]string, 0, len(st.files))
		for _, f := range st.files {
			ids = append(ids, f.ID)
		}
		snap.Members = append(snap.Members, fed.MemberSummary{
			Name: st.Name, Node: st.Node, Status: catalog.StatusActive,
			Reachable: true, FileCount: st.FileCount, IDs: ids,
		})
	}
	_ = cache.Record(snap)
}

func collectEntries(states []memberState, locFilter, pathFilter string) []entryJSON {
	var out []entryJSON
	for _, st := range states {
		if locFilter != "" && st.Name != locFilter {
			continue
		}
		for _, f := range st.files {
			if pathFilter != "" && !strings.HasPrefix(f.Path, pathFilter) {
				continue
			}
			out = append(out, entryJSON{
				ID: f.ID, Short: identity.Short(f.ID), Path: st.Name + "/" + f.Path,
				SyncMode: f.SyncMode, Size: f.Size, SHA256: f.SHA256, Cached: st.Cached,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func emitMembers(cmd *cobra.Command, fl lsFlags, states []memberState, reach fed.Reach) error {
	if fl.json {
		return emitJSON(cmd, lsJSON{Members: states, Answered: reach.Answered, Unreachable: reach.Unreachable})
	}
	w := cmd.OutOrStdout()
	for _, st := range states {
		status := "online"
		if !st.Online {
			if st.Cached {
				status = "offline — last seen " + st.LastSeen
			} else {
				status = "offline — no cached state"
			}
		}
		fmt.Fprintf(w, "%-16s %-32s %d files, %s\n", st.Name, status, st.FileCount, humanBytes(st.Size))
	}
	fmt.Fprint(w, reachFooter(reach))
	return nil
}

func emitEntries(cmd *cobra.Command, fl lsFlags, entries []entryJSON, reach fed.Reach) error {
	if fl.json {
		return emitJSON(cmd, lsJSON{Entries: entries, Answered: reach.Answered, Unreachable: reach.Unreachable})
	}
	w := cmd.OutOrStdout()
	for _, e := range entries {
		if fl.idsOnly {
			id := e.Short
			if fl.long {
				id = e.ID
			}
			fmt.Fprintln(w, id)
			continue
		}
		id := e.Short
		if fl.long {
			id = e.ID
		}
		stale := ""
		if e.Cached {
			stale = " (cached)"
		}
		fmt.Fprintf(w, "%-14s %-7s %10s  %s%s\n", id, e.SyncMode, humanBytes(e.Size), e.Path, stale)
	}
	fmt.Fprint(w, reachFooter(reach))
	return nil
}

func emitJSON(cmd *cobra.Command, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(b))
	return nil
}

func reachFooter(reach fed.Reach) string {
	total := len(reach.Answered) + len(reach.Unreachable)
	s := fmt.Sprintf("— %d/%d members answered", len(reach.Answered), total)
	if len(reach.Unreachable) > 0 {
		s += fmt.Sprintf("; offline (showing cached where available): %v", reach.Unreachable)
	}
	return s + "\n"
}

// cacheDir is the advisory client cache dir for a federation: ~/.tailvault/cache/
// fed-<fed_id> (SPEC v2 §14). A home-dir failure yields a temp-relative path so
// the advisory cache degrades gracefully rather than failing the read command.
func cacheDir(fedID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".tailvault", "cache", "fed-"+fedID)
}
