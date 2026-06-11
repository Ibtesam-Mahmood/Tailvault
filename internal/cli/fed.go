package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/fed"
	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// newFedCmd is the federation membership command group. The roster lives in each
// member's catalog [federation] section (no central registry — serverless) and is
// mirrored into the client's advisory caches. Membership changes are client-driven
// WAL ops fanned out to every member: reachable members apply immediately,
// unreachable members are reported pending and converge on a later contact / `ops
// retry`. There is no "federation down" — a change succeeds as soon as the
// reachable members record it (D6/D27).
func newFedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fed",
		Short: "Manage federation membership (init/join/leave/evict/status)",
	}
	cmd.AddCommand(
		newFedInitCmd(),
		newFedJoinCmd(),
		newFedLeaveCmd(),
		newFedEvictCmd(),
		newFedStatusCmd(),
	)
	return cmd
}

// ---- fed init --------------------------------------------------------------

// newFedInitCmd: `tailvault fed init <location>` — mint a federation around one
// existing vault location. NOT password-gated: it is the bootstrap (like vault
// init/put), it writes only the founding node's own catalog, and no password can
// exist before the federation does (§16 enumerates join/leave/evict, not init).
func newFedInitCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "init <location>",
		Short: "Create a federation around an existing vault location",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFedInit(cmd, args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

func runFedInit(cmd *cobra.Command, locName string, jsonOut bool) error {
	ctx := cmd.Context()
	b, loc, err := locationBackend(locName)
	if err != nil {
		return err
	}
	cat, err := readCatalog(ctx, b)
	if err != nil {
		return tserr.NodeOfflineErr(loc.Node, err)
	}
	if cat == nil {
		return tserr.ConfigErr("fed init: "+locName+" is not initialised; run `tailvault vault init "+locName+"` first", nil)
	}
	if cat.Federation.FedID != "" {
		return tserr.ConfigErr("fed init: "+locName+" is already in federation "+cat.Federation.FedID, nil)
	}

	now := time.Now().UTC()
	actor := initActor(cmd)
	self := catalog.Member{Name: locName, Node: loc.Node, JoinedAt: now, Status: catalog.StatusActive}

	// fed_id = sha256 of the canonical seq-0 genesis WAL entry (SPEC v2 §13). The
	// derivation entry is constructed canonically (seq 0, prev = zero) so the id is
	// well-defined even when the vault's WAL already holds ingest ops — see handoff.
	opID := rosterOpID("", "init", locName)
	genesis := wal.Entry{
		Seq: 0, OpID: opID, PrevHash: wal.ZeroHash, OpType: wal.OpRoster,
		Actor: actor, CreatedAt: now,
		Args: map[string]string{"action": "init", "member": locName, "node": loc.Node},
	}
	fedID, err := wal.Hash(genesis)
	if err != nil {
		return tserr.ConfigErr("fed init: mint fed id", err)
	}

	// Record the init as a real roster op on the founding node, then write the
	// [federation] section.
	log := &wal.Log{B: b}
	intent := wal.Entry{
		OpID: opID, OpType: wal.OpRoster, Actor: actor, CreatedAt: now,
		Args: map[string]string{"action": "init", "fed_id": fedID, "member": locName, "node": loc.Node, "status": catalog.StatusActive},
	}
	if err := appendFedIntent(ctx, log, intent, loc.Node, "init"); err != nil {
		return err
	}
	cat.Federation = catalog.Federation{FedID: fedID, Members: []catalog.Member{self}}
	if err := persistCatalog(ctx, b, cat, loc.Node); err != nil {
		return err
	}
	if err := log.MarkDone(ctx, opID); err != nil {
		return tserr.NodeOfflineErr(loc.Node, err)
	}

	if jsonOut {
		return emitJSON(cmd, map[string]any{"fed_id": fedID, "member": locName, "node": loc.Node})
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "federation created\n")
	fmt.Fprintf(w, "fed_id     %s\n", fedID)
	fmt.Fprintf(w, "member     %s (%s)\n", locName, loc.Node)
	return nil
}

// ---- fed join --------------------------------------------------------------

// newFedJoinCmd: `tailvault fed join <location> [--via <member>]` — add a location
// to an existing federation by fanning the roster-add out to every member. Each
// member's write is gated by THAT member's password (§16 / Task 27 ruling).
func newFedJoinCmd() *cobra.Command {
	var via, passwordFile string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "join <location>",
		Short: "Join a location to an existing federation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFedJoin(cmd, args[0], fedFlags{via: via, passwordFile: passwordFile, json: jsonOut})
		},
	}
	cmd.Flags().StringVar(&via, "via", "", "sponsoring member to discover the roster from (default: any registered member)")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "read member passwords from this file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

type fedFlags struct {
	via          string
	passwordFile string
	json         bool
}

func runFedJoin(cmd *cobra.Command, locName string, fl fedFlags) error {
	ctx := cmd.Context()
	reg, err := locations.Load()
	if err != nil {
		return tserr.ConfigErr("load locations.toml", err)
	}
	jb, jloc, err := locationBackend(locName)
	if err != nil {
		return err
	}
	jcat, err := readCatalog(ctx, jb)
	if err != nil {
		return tserr.NodeOfflineErr(jloc.Node, err)
	}
	if jcat == nil {
		return tserr.ConfigErr("fed join: "+locName+" is not initialised; run `tailvault vault init "+locName+"` first", nil)
	}

	roster, err := discoverRoster(ctx, reg, fl.via, locName)
	if err != nil {
		return err
	}
	if hasActiveMember(roster, locName) {
		// Idempotent: already an active member — no-op success (re-running join is safe).
		return emitFedChange(cmd, fl.json, "already a member of", roster.FedID, locName, []string{locName}, nil, "")
	}

	now := time.Now().UTC()
	actor := initActor(cmd)
	self := catalog.Member{Name: locName, Node: jloc.Node, JoinedAt: now, Status: catalog.StatusActive}
	newMembers := upsertMember(roster.Members, self)

	// Fan the roster-add out to every existing active member (gated each), then
	// write the joiner's own catalog with the full roster snapshot.
	applied, pending, err := fanoutRoster(ctx, reg, roster.Active(), fanArgs{
		action: "join", targetName: locName, targetNode: jloc.Node, fedID: roster.FedID,
		actor: actor, passwordFile: fl.passwordFile, newMembers: newMembers, now: now,
	})
	if err != nil {
		return err
	}
	if err := writeRoster(ctx, jb, jloc, locName, fanArgs{
		action: "join", targetName: locName, targetNode: jloc.Node, fedID: roster.FedID,
		actor: actor, passwordFile: fl.passwordFile, newMembers: newMembers, now: now,
	}); err != nil {
		return err
	}
	applied = append(applied, locName)

	return emitFedChange(cmd, fl.json, "joined", roster.FedID, locName, applied, pending, "")
}

// ---- fed leave -------------------------------------------------------------

// newFedLeaveCmd: `tailvault fed leave <location>` — clean detach (D28). Marks the
// member `left` across every roster (its row is KEPT — it documents the detach);
// NO data is deleted. Referencing repos learn on next pull (the lock-v2 WARN, Task
// 35). Gated on each member written.
func newFedLeaveCmd() *cobra.Command {
	var passwordFile string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "leave <location>",
		Short: "Detach a member from its federation (no data deleted)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFedLeave(cmd, args[0], fedFlags{passwordFile: passwordFile, json: jsonOut})
		},
	}
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "read member passwords from this file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

func runFedLeave(cmd *cobra.Command, locName string, fl fedFlags) error {
	ctx := cmd.Context()
	reg, err := locations.Load()
	if err != nil {
		return tserr.ConfigErr("load locations.toml", err)
	}
	roster, err := loadRoster(ctx, reg)
	if err != nil {
		return err
	}
	m, ok := roster.Find(locName)
	if !ok {
		return tserr.ConfigErr("fed leave: "+locName+" is not a member of "+roster.FedID, nil)
	}
	if m.Status != catalog.StatusActive {
		// Idempotent: already detached — no-op success.
		return emitFedChange(cmd, fl.json, "already "+m.Status+" from", roster.FedID, locName, nil, nil, "")
	}

	now := time.Now().UTC()
	actor := initActor(cmd)
	newMembers := setMemberStatus(roster.Members, locName, catalog.StatusLeft)

	// Write the status flip to every active member (including the leaver, whose own
	// catalog keeps the `left` row).
	targets := roster.Active()
	applied, pending, err := fanoutRoster(ctx, reg, targets, fanArgs{
		action: "leave", targetName: locName, targetNode: nodeOf(roster, locName), fedID: roster.FedID,
		actor: actor, passwordFile: fl.passwordFile, newMembers: newMembers, now: now,
	})
	if err != nil {
		return err
	}

	note := "files homed at " + locName + " drop from the federated tree; referencing repos will WARN on next pull (repush to a new location or resync from a moved copy). No data was deleted — " + locName + "'s disk is untouched."
	return emitFedChange(cmd, fl.json, "left", roster.FedID, locName, applied, pending, note)
}

// ---- fed evict -------------------------------------------------------------

// newFedEvictCmd: `tailvault fed evict <member>` — declare a DEAD member departed.
// Password-gated (D9: destructive to the roster). Refuses a member that answers a
// live ping ("use fed leave on it instead"). Applies an `evicted` status flip with
// the evictor's stamp across reachable survivors; pending for the rest. The roster
// (evicted) wins over a returning member's self-claim via the merge status rank.
func newFedEvictCmd() *cobra.Command {
	var passwordFile string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "evict <member>",
		Short: "Retire a dead member from the federation (the only way)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFedEvict(cmd, args[0], fedFlags{passwordFile: passwordFile, json: jsonOut})
		},
	}
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "read surviving-member passwords from this file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

func runFedEvict(cmd *cobra.Command, member string, fl fedFlags) error {
	ctx := cmd.Context()
	reg, err := locations.Load()
	if err != nil {
		return tserr.ConfigErr("load locations.toml", err)
	}
	roster, err := loadRoster(ctx, reg)
	if err != nil {
		return err
	}
	target, ok := roster.Find(member)
	if !ok {
		return tserr.ConfigErr("fed evict: "+member+" is not a member of "+roster.FedID, nil)
	}
	if target.Status != catalog.StatusActive {
		// Idempotent: already departed (left/evicted) — no-op success.
		return emitFedChange(cmd, fl.json, "already "+target.Status+" from", roster.FedID, member, nil, nil, "")
	}

	// Refuse to evict a member that is actually reachable.
	if err := memberProbe(reg)(ctx, target); err == nil {
		return tserr.ConfigErr("fed evict: "+member+" is reachable — use `tailvault fed leave "+member+"` on it instead", nil)
	}

	now := time.Now().UTC()
	actor := initActor(cmd)
	newMembers := setMemberStatus(roster.Members, member, catalog.StatusEvicted)

	// Surviving active members (everyone except the evicted target) receive the flip.
	var survivors []catalog.Member
	for _, m := range roster.Active() {
		if m.Name != member {
			survivors = append(survivors, m)
		}
	}
	applied, pending, err := fanoutRoster(ctx, reg, survivors, fanArgs{
		action: "evict", targetName: member, targetNode: target.Node, fedID: roster.FedID,
		actor: actor, passwordFile: fl.passwordFile, newMembers: newMembers, now: now,
	})
	if err != nil {
		return err
	}
	note := member + " is now evicted; if it ever returns, the roster (evicted) wins — re-`join` it cleanly."
	return emitFedChange(cmd, fl.json, "evicted", roster.FedID, member, applied, pending, note)
}

// ---- fed status ------------------------------------------------------------

// newFedStatusCmd: `tailvault fed status` — read-only dashboard (no password):
// roster, fresh per-member reachability, cache `last seen` for non-answerers,
// outstanding pending roster ops per member, and a divergence note when members
// disagree (resolve via `ops`/retry).
func newFedStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the federation roster, reachability, and last-seen",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFedStatus(cmd, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	return cmd
}

type fedStatusRow struct {
	Name     string `json:"name"`
	Node     string `json:"node"`
	Status   string `json:"status"`
	Online   bool   `json:"online"`
	LastSeen string `json:"last_seen,omitempty"`
	Pending  int    `json:"pending_ops"`
}

type fedStatusJSON struct {
	FedID     string         `json:"fed_id"`
	Rows      []fedStatusRow `json:"members"`
	Divergent bool           `json:"divergent"`
	Answered  []string       `json:"members_answered"`
	Offline   []string       `json:"members_unreachable"`
}

func runFedStatus(cmd *cobra.Command, jsonOut bool) error {
	ctx := cmd.Context()
	reg, err := locations.Load()
	if err != nil {
		return tserr.ConfigErr("load locations.toml", err)
	}
	roster, err := loadRoster(ctx, reg)
	if err != nil {
		return err
	}

	// All roster members (not just active) — status reports left/evicted rows too.
	members := roster.Members
	reach := fed.Probe(ctx, roster.Active(), memberProbe(reg))
	answered := map[string]bool{}
	for _, n := range reach.Answered {
		answered[n] = true
	}
	cache := &fed.Cache{Dir: cacheDir(roster.FedID)}
	current, previous, _ := cache.Load()

	rows := make([]fedStatusRow, 0, len(members))
	divergent := false
	for _, m := range members {
		row := fedStatusRow{Name: m.Name, Node: m.Node, Status: m.Status, Online: answered[m.Name]}
		if row.Online {
			b, berr := backendForRegistry(reg)(m)
			if berr == nil {
				row.Pending = pendingRosterCount(ctx, b)
				if diverges(ctx, b, roster) {
					divergent = true
				}
			}
		} else if ms := lastKnownSummary(current, previous, m.Name); ms != nil {
			row.LastSeen = rfc(ms.LastSeen)
		}
		rows = append(rows, row)
	}

	if jsonOut {
		return emitJSON(cmd, fedStatusJSON{
			FedID: roster.FedID, Rows: rows, Divergent: divergent,
			Answered: reach.Answered, Offline: reach.Unreachable,
		})
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "fed_id   %s\n", roster.FedID)
	for _, r := range rows {
		state := r.Status
		live := "offline"
		if r.Online {
			live = "online"
		} else if r.LastSeen != "" {
			live = "offline — last seen " + r.LastSeen
		}
		extra := ""
		if r.Pending > 0 {
			extra = fmt.Sprintf("  (%d pending)", r.Pending)
		}
		fmt.Fprintf(w, "%-16s %-10s %-12s %s%s\n", r.Name, state, r.Node, live, extra)
	}
	if divergent {
		fmt.Fprintln(w, "warning: members disagree on the roster — run `tailvault ops` / retry to converge")
	}
	fmt.Fprint(w, reachFooter(reach))
	return nil
}

// ---- shared roster machinery ----------------------------------------------

// fanArgs bundles the inputs for a single member's roster write.
type fanArgs struct {
	action       string // join | leave | evict
	targetName   string // the member added / whose status changed
	targetNode   string
	fedID        string
	actor        string
	passwordFile string
	newMembers   []catalog.Member // the merged roster to write into each member
	now          time.Time
}

// fanoutRoster applies a roster change to every target member. It gates EVERY
// reachable target FIRST (so a single wrong password leaves the roster untouched
// everywhere — no partial write), then writes them all. Unreachable / unregistered
// targets are reported pending and converge on a later contact / `ops retry`.
func fanoutRoster(ctx context.Context, reg locations.Registry, targets []catalog.Member, a fanArgs) (applied, pending []string, err error) {
	probe := memberProbe(reg)
	type tw struct {
		m   catalog.Member
		b   backend.Backend
		loc locations.Location
	}
	var writable []tw
	for _, m := range targets {
		b, loc, e := locationBackend(m.Name)
		if e != nil || probe(ctx, m) != nil {
			pending = append(pending, m.Name)
			continue
		}
		writable = append(writable, tw{m, b, loc})
	}
	// Phase 1: gate all reachable targets before any write (all-or-nothing).
	for _, w := range writable {
		if e := gateLocation(ctx, w.loc, w.b, w.m.Name, a.passwordFile); e != nil {
			return nil, nil, e
		}
	}
	// Phase 2: write all reachable targets.
	for _, w := range writable {
		if e := writeRosterNoGate(ctx, w.b, w.loc, w.m.Name, a); e != nil {
			return applied, pending, e
		}
		applied = append(applied, w.m.Name)
	}
	sort.Strings(applied)
	sort.Strings(pending)
	return applied, pending, nil
}

// writeRoster gates then writes one member's roster (used for the initiator's own
// catalog, which fanoutRoster does not cover).
func writeRoster(ctx context.Context, b backend.Backend, loc locations.Location, member string, a fanArgs) error {
	if err := gateLocation(ctx, loc, b, member, a.passwordFile); err != nil {
		return err
	}
	return writeRosterNoGate(ctx, b, loc, member, a)
}

// writeRosterNoGate runs the WAL lifecycle for one member's roster write: OpRoster
// intent → set [federation] → persist → done. Only the [federation] section is
// replaced; the member's files are preserved. The op id is deterministic per
// (fed, action, target) so a retry on the same member dedups (idempotent).
func writeRosterNoGate(ctx context.Context, b backend.Backend, loc locations.Location, member string, a fanArgs) error {
	opID := rosterOpID(a.fedID, a.action, a.targetName)
	log := &wal.Log{B: b}
	intent := wal.Entry{
		OpID: opID, OpType: wal.OpRoster, Actor: a.actor, CreatedAt: a.now,
		Args: map[string]string{
			"action": a.action, "member": a.targetName, "node": a.targetNode, "fed_id": a.fedID,
		},
	}
	if err := appendFedIntent(ctx, log, intent, loc.Node, a.action); err != nil {
		return err
	}
	cat, err := readCatalog(ctx, b)
	if err != nil {
		return tserr.NodeOfflineErr(loc.Node, err)
	}
	if cat == nil {
		return tserr.ObjMissingErr(member+" (no catalog)", nil)
	}
	cat.Federation = catalog.Federation{FedID: a.fedID, Members: cloneMembers(a.newMembers)}
	if err := persistCatalog(ctx, b, cat, loc.Node); err != nil {
		return err
	}
	return log.MarkDone(ctx, opID)
}

// appendFedIntent maps the WAL sentinels for a roster op (mirrors appendOpIntent
// with a "fed" prefix).
func appendFedIntent(ctx context.Context, log *wal.Log, e wal.Entry, node, action string) error {
	if _, err := log.AppendIntent(ctx, e); err != nil {
		switch {
		case errors.Is(err, wal.ErrDuplicateOp):
			return nil
		case errors.Is(err, wal.ErrOpInFlight):
			return tserr.ConfigErr("fed "+action+": a roster op is already in flight — retry shortly", err)
		case errors.Is(err, wal.ErrChainBroken):
			return tserr.FedChainBrokenErr(node, err)
		default:
			return tserr.NodeOfflineErr(node, err)
		}
	}
	return nil
}

// discoverRoster finds the federation to join: from --via if given, else by
// merging every registered location's roster (loadRoster), excluding the joiner.
func discoverRoster(ctx context.Context, reg locations.Registry, via, joiner string) (fed.Roster, error) {
	if via != "" {
		b, loc, err := locationBackend(via)
		if err != nil {
			return fed.Roster{}, err
		}
		cat, err := readCatalog(ctx, b)
		if err != nil {
			return fed.Roster{}, tserr.NodeOfflineErr(loc.Node, err)
		}
		if cat == nil || cat.Federation.FedID == "" {
			return fed.Roster{}, tserr.ConfigErr("fed join: sponsor "+via+" is not in a federation", nil)
		}
		return fed.FromCatalog(cat)
	}
	r, err := loadRoster(ctx, reg)
	if err != nil {
		return fed.Roster{}, tserr.ConfigErr("fed join: no federation found to join — register a member location (with `tailvault location add`) or run `tailvault fed init`", err)
	}
	return r, nil
}

// rosterOpID is the deterministic op id for a roster change (per fed, action,
// target) — a retry re-presents the same id and the WAL dedups.
func rosterOpID(fedID, action, target string) string {
	return opIDFromParts("tailvault/fed-roster", fedID, action, target)
}

func upsertMember(members []catalog.Member, m catalog.Member) []catalog.Member {
	out := cloneMembers(members)
	for i := range out {
		if out[i].Name == m.Name {
			out[i] = m
			return out
		}
	}
	return append(out, m)
}

func setMemberStatus(members []catalog.Member, name, status string) []catalog.Member {
	out := cloneMembers(members)
	for i := range out {
		if out[i].Name == name {
			out[i].Status = status
		}
	}
	return out
}

func cloneMembers(members []catalog.Member) []catalog.Member {
	out := make([]catalog.Member, len(members))
	copy(out, members)
	return out
}

func hasActiveMember(r fed.Roster, name string) bool {
	m, ok := r.Find(name)
	return ok && m.Status == catalog.StatusActive
}

func nodeOf(r fed.Roster, name string) string {
	if m, ok := r.Find(name); ok {
		return m.Node
	}
	return ""
}

// pendingRosterCount counts outstanding (intent-state) roster ops on a member.
func pendingRosterCount(ctx context.Context, b backend.Backend) int {
	pend, err := (&wal.Log{B: b}).Pending(ctx, "")
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range pend {
		if r.Entry.OpType == wal.OpRoster {
			n++
		}
	}
	return n
}

// diverges reports whether a member's roster differs from the merged roster
// (membership/status signature) — the partition-divergence signal.
func diverges(ctx context.Context, b backend.Backend, merged fed.Roster) bool {
	cat, err := readCatalog(ctx, b)
	if err != nil || cat == nil {
		return false
	}
	r, err := fed.FromCatalog(cat)
	if err != nil {
		return false
	}
	return rosterSig(r) != rosterSig(merged)
}

func rosterSig(r fed.Roster) string {
	parts := make([]string, 0, len(r.Members))
	for _, m := range r.Members {
		parts = append(parts, m.Name+":"+m.Status)
	}
	sort.Strings(parts)
	return r.FedID + "|" + strings.Join(parts, ",")
}

// emitFedChange renders a membership-change result (join/leave/evict).
func emitFedChange(cmd *cobra.Command, jsonOut bool, verb, fedID, member string, applied, pending []string, note string) error {
	if jsonOut {
		return emitJSON(cmd, map[string]any{
			"fed_id": fedID, "member": member, "result": verb,
			"applied": applied, "pending": pending, "note": note,
		})
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "%s %s (fed %s)\n", member, verb, fedID)
	fmt.Fprintf(w, "applied    %s\n", strings.Join(applied, ", "))
	if len(pending) > 0 {
		fmt.Fprintf(w, "pending    %s (offline — converge on next contact or `tailvault ops retry`)\n", strings.Join(pending, ", "))
	}
	if note != "" {
		fmt.Fprintf(w, "note       %s\n", note)
	}
	return nil
}
