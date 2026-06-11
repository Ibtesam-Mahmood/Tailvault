// Package fed is the serverless federation layer: it reads and merges the
// roster that lives in each member's catalog [federation] section (SPEC v2 §13),
// maintains advisory client state caches (§14), and accounts for which members
// answered a fan-out (per-operation reachability scoping, D27). It holds no
// global state and pings nothing on its own — callers inject the prober and the
// per-member querier, so nodes stay passive and tests never touch a real node.
//
// Caches are advisory, never authoritative (D26): a cache hit only colors error
// messages ("last seen on pi-2"); live pings always decide the outcome class.
package fed

import (
	"errors"
	"fmt"
	"sort"

	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
)

// Member is an alias for catalog.Member. SPEC v2 §8b reserves the name
// fed.Member, but §9/§13 make the catalog the serialization home of the roster
// wire types; catalog is a leaf package (fed depends on catalog, never the
// reverse), so aliasing here is the only cycle-free way to honor both. There is
// exactly one underlying type — fed.Member and catalog.Member are identical.
type Member = catalog.Member

// Roster is the merged federation view (SPEC v2 §13). Members is kept sorted by
// Name byte-wise ascending so every operation over a roster is deterministic.
type Roster struct {
	FedID   string
	Members []catalog.Member
}

// FromCatalog lifts a single catalog's [federation] section into a Roster. It
// errors on an empty fed_id (an un-federated or malformed catalog has no roster
// to read).
func FromCatalog(c *catalog.Catalog) (Roster, error) {
	if c == nil {
		return Roster{}, errors.New("fed: FromCatalog: nil catalog")
	}
	if c.Federation.FedID == "" {
		return Roster{}, errors.New("fed: FromCatalog: catalog has empty fed_id")
	}
	r := Roster{
		FedID:   c.Federation.FedID,
		Members: append([]catalog.Member(nil), c.Federation.Members...),
	}
	r.sortMembers()
	return r, nil
}

// Merge combines rosters read from several members' catalogs into one view.
// All non-empty fed_ids must agree (a mismatch means two different federations
// were mixed — a hard error, never silently pick one). Member rows are unioned
// by name; on a conflict the newest joined_at wins, breaking ties by status
// rank (a terminal status — evicted over left over active — wins a timestamp
// collision, since you cannot un-leave by re-merging an older active row). The
// output is deterministic regardless of input order.
func Merge(rosters ...Roster) (Roster, error) {
	if len(rosters) == 0 {
		return Roster{}, errors.New("fed: Merge requires at least one roster")
	}

	var fedID string
	byName := make(map[string]catalog.Member)
	for _, r := range rosters {
		if r.FedID != "" {
			switch {
			case fedID == "":
				fedID = r.FedID
			case r.FedID != fedID:
				return Roster{}, fmt.Errorf("fed: roster fed_id mismatch: %q vs %q", fedID, r.FedID)
			}
		}
		for _, m := range r.Members {
			if cur, ok := byName[m.Name]; !ok || memberWins(m, cur) {
				byName[m.Name] = m
			}
		}
	}
	if fedID == "" {
		return Roster{}, errors.New("fed: Merge: no fed_id among rosters")
	}

	out := Roster{FedID: fedID, Members: make([]catalog.Member, 0, len(byName))}
	for _, m := range byName {
		out.Members = append(out.Members, m)
	}
	out.sortMembers()
	return out, nil
}

// Active returns only members with status "active" — the set fan-out and the gc
// all-members gate operate on. left/evicted members are retained in the merged
// roster (their history feeds WARN messages, D28) but excluded here.
func (r Roster) Active() []catalog.Member {
	var out []catalog.Member
	for _, m := range r.Members {
		if m.Status == "active" {
			out = append(out, m)
		}
	}
	return out
}

// Find returns the member with the given name and true, or a zero Member and
// false when absent.
func (r Roster) Find(name string) (catalog.Member, bool) {
	for _, m := range r.Members {
		if m.Name == name {
			return m, true
		}
	}
	return catalog.Member{}, false
}

// UnregisteredMembers reports roster members whose name is absent from the
// caller-supplied set of registered location names, so a command can hint
// `tailvault location add <name>`. It NEVER writes the registry — that file
// holds user-confirmed node addresses and credentials (§4). The registered set
// is passed in rather than read from internal/locations so this package stays
// decoupled from registry internals. The result preserves roster order (sorted
// by name).
func UnregisteredMembers(r Roster, registered []string) []catalog.Member {
	have := make(map[string]struct{}, len(registered))
	for _, n := range registered {
		have[n] = struct{}{}
	}
	var out []catalog.Member
	for _, m := range r.Members {
		if _, ok := have[m.Name]; !ok {
			out = append(out, m)
		}
	}
	return out
}

func (r *Roster) sortMembers() {
	sort.SliceStable(r.Members, func(i, j int) bool {
		return r.Members[i].Name < r.Members[j].Name
	})
}

// memberWins reports whether candidate should replace current when unioning two
// rows for the same member name: newer joined_at wins; on a tie the higher
// status rank wins (terminal statuses stick).
func memberWins(cand, cur catalog.Member) bool {
	if cand.JoinedAt.After(cur.JoinedAt) {
		return true
	}
	if cand.JoinedAt.Before(cur.JoinedAt) {
		return false
	}
	return statusRank(cand.Status) > statusRank(cur.Status)
}

// statusRank orders membership statuses so a terminal status wins a joined_at
// tie. Unknown statuses rank with active (0) — they are treated conservatively
// as not-yet-terminal.
func statusRank(status string) int {
	switch status {
	case "evicted":
		return 2
	case "left":
		return 1
	default: // "active" and any unknown/future value
		return 0
	}
}
