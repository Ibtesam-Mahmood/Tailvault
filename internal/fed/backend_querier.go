package fed

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	"github.com/Ibtesam-Mahmood/tailvault/internal/catalog"
	"github.com/Ibtesam-Mahmood/tailvault/internal/wal"
)

// catalogKey is the store-relative key of a member's catalog (SPEC v2 §9).
const catalogKey = "meta/catalog.toml"

// BackendFor resolves a federation member to the backend that reads its vault.
// The locations.toml / registry resolution that turns a member into a concrete
// backend stays in the caller's command layer; fed only needs the resolved
// backend. It is a fed-owned named type so no lower package ever imports the cli
// layer (which would invert the dependency direction).
type BackendFor func(catalog.Member) (backend.Backend, error)

// BackendQuerier is the production fed.Querier: it answers a resolution query by
// reading a member's catalog and WAL over that member's backend. The member
// executes nothing — this is a pure read (serverless). It is shared by every
// command that resolves a file (vault get/stat/mv/rm) so the catalog+WAL read
// semantics and the wal.ErrChainBroken handling are identical everywhere, rather
// than re-derived per command.
type BackendQuerier struct {
	backendFor BackendFor
}

// NewBackendQuerier builds a BackendQuerier over the given member→backend seam.
func NewBackendQuerier(backendFor BackendFor) *BackendQuerier {
	return &BackendQuerier{backendFor: backendFor}
}

// Query implements Querier. It reports:
//   - Found + File when the member's catalog currently holds id;
//   - MovedTo when the member no longer holds id but a COMPLETED cross-member
//     move in its WAL forwards id elsewhere (cross-member moves carry
//     args["moved_to"]; a local rename carries only from/to and is deliberately
//     NOT a forwarding pointer — it stays on the same member);
//   - PendingMove when an in-flight (intent) move op references id.
//
// A broken WAL chain is wrapped with the member name and stays
// errors.Is(err, wal.ErrChainBroken)==true, so the command boundary maps it to
// tserr.FedChainBrokenErr (exit 6) in one place.
func (q *BackendQuerier) Query(ctx context.Context, m catalog.Member, id string) (MemberView, error) {
	b, err := q.backendFor(m)
	if err != nil {
		return MemberView{}, fmt.Errorf("fed: backend for member %q: %w", m.Name, err)
	}

	cat, err := loadCatalog(ctx, b)
	if err != nil {
		return MemberView{}, fmt.Errorf("fed: read catalog on member %q: %w", m.Name, err)
	}
	if cat != nil {
		if f, ok := cat.FindID(id); ok {
			// Still held here (even if a move intent is pending, the source remains
			// readable until the move completes) → Found wins.
			return MemberView{Member: m.Name, File: f, Found: true}, nil
		}
	}

	// Not held: a single chain-verified WAL read tells us whether a move is in
	// flight (PendingMove) or completed to another member (MovedTo).
	recs, err := (&wal.Log{B: b}).Read(ctx)
	if err != nil {
		return MemberView{}, fmt.Errorf("fed: read WAL on member %q: %w", m.Name, err)
	}
	view := MemberView{Member: m.Name}
	for _, r := range recs { // ascending seq → the latest completed move wins
		if r.Entry.OpType != wal.OpMove || !contains(r.Entry.BlobRefs, id) {
			continue
		}
		switch r.State {
		case wal.StateIntent:
			view.PendingMove = true
		case wal.StateDone:
			if to := r.Entry.Args["moved_to"]; to != "" {
				view.MovedTo = to
			}
		}
	}
	return view, nil
}

// loadCatalog reads + parses a member's catalog over the backend. A member with
// no catalog yet (e.g. mid-bootstrap) is treated as holding nothing (nil, nil)
// rather than an error, so one un-catalogued member never fails a
// federation-wide resolution — reachability, not catalog presence, is what
// proves or disproves absence.
func loadCatalog(ctx context.Context, b backend.Backend) (*catalog.Catalog, error) {
	var buf bytes.Buffer
	if err := b.Get(ctx, catalogKey, &buf); err != nil {
		if errors.Is(err, backend.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return catalog.Parse(buf.Bytes())
}
