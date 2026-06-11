// Package setup wires the human-facing node-registration flow: enumerate the
// online peers the local Tailscale session already sees, present a pick-list,
// and build a locations.Location to persist. It reads ONLY the local,
// already-authenticated session — never a Tailscale login or API call (Q9 /
// Non-Goals). Discovery failure is non-fatal: it falls back to manual entry.
package setup

import (
	"sort"

	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
)

// Peer is a selectable node in the pick-list.
type Peer struct {
	Name string // MagicDNS DNSName, trailing dot trimmed
	IP   string // first TailscaleIP (100.x) as a fallback label
}

// OnlinePeers returns the online peers from an already-fetched status, sorted by
// Name. It returns (nil, false) when the daemon is absent/logged out (statusErr
// != nil) or no online peers exist, signalling the caller to fall back to manual
// entry. Pure: takes the fetched status + its error, so tests need no daemon.
func OnlinePeers(st tailscale.Status, statusErr error) ([]Peer, bool) {
	if statusErr != nil || !st.LoggedIn {
		return nil, false
	}
	var peers []Peer
	for _, p := range st.Peers {
		if !p.Online {
			continue
		}
		name := p.DNSName
		ip := ""
		if len(p.IPs) > 0 {
			ip = p.IPs[0]
		}
		if name == "" {
			name = ip // fall back to the IP as the label when MagicDNS is empty
		}
		if name == "" {
			continue // nothing to show / address by
		}
		peers = append(peers, Peer{Name: name, IP: ip})
	}
	if len(peers) == 0 {
		return nil, false
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })
	return peers, true
}
