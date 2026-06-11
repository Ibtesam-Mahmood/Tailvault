package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/identity"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

// catalogStoreKey is the store-relative key of a member's catalog (SPEC v2 §9).
const catalogStoreKey = "meta/catalog.toml"

// loadRoster discovers the client's federation view by reading the [federation]
// section from each registered location's catalog and merging them (fed.Merge:
// newest joined_at wins, terminal status sticks, order-independent). The roster
// is replicated across members, so reading any one yields it; merging all
// registered locations tolerates a stale or partially-updated member. A location
// whose catalog is unreachable, absent, or un-federated is skipped (best-effort
// discovery). An error is returned only when NO registered location yields a
// roster — there is then no federation to operate on.
func loadRoster(ctx context.Context, reg locations.Registry) (fed.Roster, error) {
	names := make([]string, 0, len(reg.Locations))
	for name := range reg.Locations {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic read order (Merge is order-independent anyway)

	var rosters []fed.Roster
	for _, name := range names {
		b, err := backendForLocation(reg.Locations[name], name)
		if err != nil {
			continue
		}
		var buf bytes.Buffer
		if err := b.Get(ctx, catalogStoreKey, &buf); err != nil {
			continue // unreachable / no catalog yet — best-effort
		}
		cat, err := catalog.Parse(buf.Bytes())
		if err != nil {
			continue
		}
		r, err := fed.FromCatalog(cat)
		if err != nil {
			continue // un-federated catalog (no fed_id)
		}
		rosters = append(rosters, r)
	}
	if len(rosters) == 0 {
		return fed.Roster{}, tserr.ConfigErr("no federation roster found — no registered location has a catalog with a [federation] section", nil)
	}
	return fed.Merge(rosters...)
}

// backendForRegistry returns the fed.BackendFor seam the resolution engine needs:
// it maps a federation member to the backend that reads its vault, resolving the
// member's name against the user's locations.toml. A member the user has not
// registered is a clean config error pointing at `location add` (we never invent
// node addresses — that file holds user-confirmed infra, SPEC §4). The member's
// vault root is the registered base_path; the per-repo subpath is NOT applied to
// federation browsing (a member's catalog lives at its own base_path/meta).
func backendForRegistry(reg locations.Registry) fed.BackendFor {
	return func(m catalog.Member) (backend.Backend, error) {
		loc, ok := reg.Locations[m.Name]
		if !ok {
			return nil, tserr.ConfigErr(fmt.Sprintf("federation member %q is not in your locations.toml — run `tailvault location add %s`", m.Name, m.Name), nil)
		}
		return backendForLocation(loc, m.Name)
	}
}

// buildResolver assembles the federation resolution engine from a registry +
// merged roster (the same wiring pull / vault stat / heal all use): a backend
// querier over the registry and the tailscale/taildrive reachability probe.
func buildResolver(reg locations.Registry, roster fed.Roster) *fed.Resolver {
	return &fed.Resolver{
		Roster: roster,
		Q:      fed.NewBackendQuerier(backendForRegistry(reg)),
		Probe:  memberProbe(reg),
	}
}

// locationBackend resolves a location BY NAME (from locations.toml) to its
// backend plus the Location record. Shared by every command that acts on a named
// location directly — vault get/put/mv/rm/passwd and track's vault-mode. An
// unknown name is a TV-CFG-01 pointing at the registry.
func locationBackend(name string) (backend.Backend, locations.Location, error) {
	reg, err := locations.Load()
	if err != nil {
		return nil, locations.Location{}, tserr.ConfigErr("load locations.toml", err)
	}
	loc, ok := reg.Locations[name]
	if !ok {
		return nil, locations.Location{}, tserr.ConfigErr("unknown storage location "+name+" (not in locations.toml)", nil)
	}
	b, err := backendForLocation(loc, name)
	return b, loc, err
}

// testBackendFor is a TEST SEAM (nil in production). When installed via
// SetTestBackendFor, backendForLocation returns the override it yields for a
// location by name. Because EVERY command's data access AND memberProbe's
// reachability check route through backendForLocation, this single seam lets the
// fedtest harness supply a down-aware backend (m.Backend()) so a CLI-driven
// command honors harness SetDown end-to-end — the reachability scenarios must
// drive the REAL CLI (7b), and the CLI's own taildrive construction can't see
// SetDown otherwise. Returning (nil, false) for a name leaves production
// construction in force. nil seam ⇒ production behavior is byte-for-byte unchanged.
// Keyed by location name (the harness's f.MemberBackend(name) lookup); the
// location record isn't needed — the override already encapsulates the node.
var testBackendFor func(name string) (backend.Backend, bool)

// SetTestBackendFor installs (or clears, with nil) the test backend seam.
// TEST-ONLY: production never calls it; when nil, backendForLocation builds the
// real ssh/taildrive backend exactly as before.
func SetTestBackendFor(fn func(name string) (backend.Backend, bool)) {
	testBackendFor = fn
}

// backendForLocation constructs a Backend from a registered location, rooted at
// its base_path (no subpath — federation member vaults are browsed at their own
// root). Mirrors resolveBackend's per-backend construction.
func backendForLocation(loc locations.Location, name string) (backend.Backend, error) {
	if testBackendFor != nil {
		if b, ok := testBackendFor(name); ok {
			return b, nil // harness-supplied (down-aware) backend
		}
	}
	switch loc.Backend {
	case locations.BackendSSH:
		if loc.User == "" {
			return nil, tserr.ConfigErr("ssh location "+name+" missing user", nil)
		}
		return &backend.SSH{User: loc.User, Node: loc.Node, BasePath: loc.BasePath, Ping: tailscale.New().Ping}, nil
	case locations.BackendTaildrive:
		if loc.Share == "" {
			return nil, tserr.ConfigErr("taildrive location "+name+" missing share", nil)
		}
		return backend.NewTaildrive(loc.BasePath), nil
	default:
		return nil, tserr.ConfigErr("location "+name+" has unknown backend", nil)
	}
}

// memberProbe is the fed.Resolver Probe seam: it reports a member reachable when
// a tailscale ping (ssh) or a base_path directory check (taildrive) succeeds. The
// member is resolved to its location via the registry; an unregistered member is
// treated as unreachable (it cannot be probed), never a hard error — partial-view
// accounting decides the outcome.
func memberProbe(reg locations.Registry) func(ctx context.Context, m catalog.Member) error {
	ts := tailscale.New()
	return func(ctx context.Context, m catalog.Member) error {
		loc, ok := reg.Locations[m.Name]
		if !ok {
			return fmt.Errorf("member %q not registered", m.Name)
		}
		switch loc.Backend {
		case locations.BackendTaildrive:
			// A passive share: reachable iff its mounted base_path is a directory.
			b, err := backendForLocation(loc, m.Name)
			if err != nil {
				return err
			}
			_, err = b.Stat(ctx, "meta/catalog.toml")
			return err
		default:
			return ts.Ping(ctx, loc.Node)
		}
	}
}

// readCatalog reads + parses a member's catalog over its backend. A member with
// no catalog yet is treated as holding nothing (nil, nil), not an error.
func readCatalog(ctx context.Context, b backend.Backend) (*catalog.Catalog, error) {
	var buf bytes.Buffer
	if err := b.Get(ctx, catalogStoreKey, &buf); err != nil {
		if errors.Is(err, backend.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return catalog.Parse(buf.Bytes())
}

// fileByPath looks up a logical path "<loc>/<rel>" in location loc's catalog and
// returns the file plus loc as the recorded home hint. A missing location is a
// config error; an absent catalog or unknown rel-path is TV-OBJ-01 (the file is
// not a known federated entry at that location).
func fileByPath(ctx context.Context, reg locations.Registry, loc, rel string) (catalog.File, string, error) {
	l, ok := reg.Locations[loc]
	if !ok {
		return catalog.File{}, "", tserr.ConfigErr("unknown location "+loc+" (not in locations.toml)", nil)
	}
	b, err := backendForLocation(l, loc)
	if err != nil {
		return catalog.File{}, "", err
	}
	cat, err := readCatalog(ctx, b)
	if err != nil {
		return catalog.File{}, "", err
	}
	if cat == nil {
		return catalog.File{}, "", tserr.ObjMissingErr(loc+"/"+rel, nil)
	}
	if f, ok := cat.Find(rel); ok {
		return f, loc, nil
	}
	return catalog.File{}, "", tserr.ObjMissingErr(loc+"/"+rel, nil)
}

// fileByIDPrefix disambiguates a (short) hex ID prefix against every active
// member's catalog, mirroring git's unique-prefix rule. Zero matches → TV-OBJ-01;
// more than one DISTINCT id → an ambiguous-prefix config error listing the
// candidates; exactly one → that file plus the member holding it as the home
// hint. A member whose backend or catalog is unreadable is skipped (a single
// unreachable member never fails the whole lookup — reachability is the
// resolver's concern, not prefix matching).
func fileByIDPrefix(ctx context.Context, reg locations.Registry, roster fed.Roster, prefix string) (catalog.File, string, error) {
	prefix = strings.ToLower(prefix)
	bf := backendForRegistry(reg)
	matches := map[string]catalog.File{} // id -> file
	homeOf := map[string]string{}        // id -> member
	for _, m := range roster.Active() {
		b, err := bf(m)
		if err != nil {
			continue
		}
		cat, err := readCatalog(ctx, b)
		if err != nil || cat == nil {
			continue
		}
		for _, f := range cat.Files {
			if strings.HasPrefix(strings.ToLower(f.ID), prefix) {
				matches[f.ID] = f
				homeOf[f.ID] = m.Name
			}
		}
	}
	switch len(matches) {
	case 0:
		return catalog.File{}, "", tserr.ObjMissingErr(prefix, nil)
	case 1:
		for id, f := range matches {
			return f, homeOf[id], nil
		}
	}
	ids := make([]string, 0, len(matches))
	for id := range matches {
		ids = append(ids, identity.Short(id))
	}
	sort.Strings(ids)
	return catalog.File{}, "", tserr.ConfigErr("ambiguous id prefix "+prefix+" matches "+strings.Join(ids, ", "), nil)
}

// resolveOutcome maps a fed.Result's Outcome to the command-boundary error (SPEC
// §8 layering: the engine returns a plain Outcome; the command owns the tserr
// mapping). A FoundElsewhere is NOT an error — it returns nil with warn=true so
// the caller can print the heal hint and proceed. id is the full file ID; the
// short form is shown to the user.
func resolveOutcome(res fed.Result, id string) (warn bool, err error) {
	switch res.Outcome {
	case fed.FoundAtHome:
		return false, nil
	case fed.FoundElsewhere:
		return true, nil // success + WARN ("home moved — run `tailvault heal`")
	case fed.PartialView:
		return false, tserr.FedPartialViewErr(identity.Short(id), res.Reach.Unreachable, nil) // exit 6
	case fed.Missing:
		return false, tserr.ObjMissingErr(identity.Short(id), nil) // exit 5
	default:
		return false, fmt.Errorf("vault: unexpected resolution outcome %s for %s", res.Outcome, identity.Short(id))
	}
}

// healWarning is the line printed for a FoundElsewhere result (SPEC v2 §15).
func healWarning(id string) string {
	return fmt.Sprintf("warning: %s found at a non-home member — home moved; run `tailvault heal`", identity.Short(id))
}

// target is a parsed `vault stat|get|mv|rm` target: either a federation file ID
// (or unambiguous short-ID prefix) or a logical path "<location>/<rel/path>".
type target struct {
	isID bool
	id   string // full or prefix hex when isID
	loc  string // location segment when a path
	rel  string // relative path within the location when a path
	raw  string
}

// parseTarget classifies a user-supplied target. A token that is all-hex and at
// least the 12-char short-ID length, with no slash, is treated as an ID/prefix;
// anything containing a slash, or non-hex, is a logical path "<loc>/<rel>". This
// mirrors git's "looks like a hash" heuristic; ambiguous-prefix resolution and
// path lookup happen against the catalog at the call site.
func parseTarget(s string) (target, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return target{}, tserr.ConfigErr("vault: empty target", nil)
	}
	if !strings.Contains(s, "/") && isHexPrefix(s) && len(s) >= shortIDLen {
		return target{isID: true, id: strings.ToLower(s), raw: s}, nil
	}
	loc, rel, _ := strings.Cut(s, "/")
	if loc == "" {
		return target{}, tserr.ConfigErr("vault: target "+s+" has no location segment", nil)
	}
	return target{loc: loc, rel: rel, raw: s}, nil
}

// shortIDLen is the 12-hex short display/lookup length (identity.Short).
const shortIDLen = 12

func isHexPrefix(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
