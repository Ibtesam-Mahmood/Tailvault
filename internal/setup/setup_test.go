package setup

import (
	"errors"
	"strings"
	"testing"

	"github.com/Ibtesam-Mahmood/tailvault/internal/locations"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tailscale"
)

// fixtureStatus mirrors the task-08 status fixture: 2 online peers + 1 offline.
func fixtureStatus() tailscale.Status {
	return tailscale.Status{
		LoggedIn: true,
		Self:     tailscale.Peer{DNSName: "laptop.tailnet-name.ts.net", Online: true},
		Peers: []tailscale.Peer{
			{DNSName: "home-pi.tailnet-name.ts.net", Online: true, IPs: []string{"100.64.0.2"}},
			{DNSName: "office-nas.tailnet-name.ts.net", Online: false},
			{DNSName: "media.tailnet-name.ts.net", Online: true},
		},
	}
}

func TestOnlinePeers_FiltersAndSorts(t *testing.T) {
	peers, ok := OnlinePeers(fixtureStatus(), nil)
	if !ok {
		t.Fatal("OnlinePeers ok=false, want true")
	}
	// office-nas is offline -> excluded; remaining sorted by Name.
	want := []string{"home-pi.tailnet-name.ts.net", "media.tailnet-name.ts.net"}
	if len(peers) != len(want) {
		t.Fatalf("got %d peers, want %d (%+v)", len(peers), len(want), peers)
	}
	for i, w := range want {
		if peers[i].Name != w {
			t.Errorf("peers[%d].Name = %q, want %q", i, peers[i].Name, w)
		}
	}
	if peers[0].IP != "100.64.0.2" {
		t.Errorf("home-pi IP = %q, want 100.64.0.2", peers[0].IP)
	}
}

func TestOnlinePeers_DaemonDown(t *testing.T) {
	if _, ok := OnlinePeers(tailscale.Status{}, errors.New("daemon down")); ok {
		t.Error("OnlinePeers ok=true on daemon error, want false")
	}
	if _, ok := OnlinePeers(tailscale.Status{LoggedIn: false}, nil); ok {
		t.Error("OnlinePeers ok=true when logged out, want false")
	}
}

// scriptedPrompter returns canned answers and records SelectPeer calls.
type scriptedPrompter struct {
	pick        int // 1-based selection
	strings     map[string]string
	backend     string
	selectCalls int
}

func (s *scriptedPrompter) SelectPeer(peers []Peer) (Peer, error) {
	s.selectCalls++
	return peers[s.pick-1], nil
}
func (s *scriptedPrompter) AskString(label, def string) (string, error) {
	if v, ok := s.strings[label]; ok {
		return v, nil
	}
	return def, nil
}
func (s *scriptedPrompter) AskBackend() (string, error) { return s.backend, nil }

func TestBuildLocation_DiscoverySelection(t *testing.T) {
	peers, _ := OnlinePeers(fixtureStatus(), nil)
	sp := &scriptedPrompter{
		pick:    1, // home-pi
		strings: map[string]string{"base_path": "/mnt/ssd/tailvault", "user": "ibte"},
		backend: "ssh",
	}
	loc, err := BuildLocation(sp, peers, "")
	if err != nil {
		t.Fatalf("BuildLocation: %v", err)
	}
	if loc.Node != "home-pi.tailnet-name.ts.net" {
		t.Errorf("Node = %q, want home-pi...", loc.Node)
	}
	if loc.BasePath != "/mnt/ssd/tailvault" || loc.Backend != locations.BackendSSH || loc.User != "ibte" {
		t.Errorf("loc = %+v", loc)
	}
	if sp.selectCalls != 1 {
		t.Errorf("SelectPeer calls = %d, want 1", sp.selectCalls)
	}
}

func TestBuildLocation_NodeFlagBypassesPicklist(t *testing.T) {
	peers, _ := OnlinePeers(fixtureStatus(), nil)
	sp := &scriptedPrompter{strings: map[string]string{"base_path": "/v", "user": "u"}, backend: "ssh"}
	loc, err := BuildLocation(sp, peers, "100.92.14.7")
	if err != nil {
		t.Fatalf("BuildLocation: %v", err)
	}
	if loc.Node != "100.92.14.7" {
		t.Errorf("Node = %q, want 100.92.14.7", loc.Node)
	}
	if sp.selectCalls != 0 {
		t.Errorf("SelectPeer calls = %d, want 0 (--node bypass)", sp.selectCalls)
	}
}

func TestBuildLocation_ManualPath_NoPeers(t *testing.T) {
	sp := &scriptedPrompter{
		strings: map[string]string{
			"node (MagicDNS name or 100.x IP)": "manual-node",
			"base_path":                        "/v",
			"user":                             "u",
		},
		backend: "ssh",
	}
	loc, err := BuildLocation(sp, nil, "") // no peers, no --node
	if err != nil {
		t.Fatalf("BuildLocation: %v", err)
	}
	if loc.Node != "manual-node" {
		t.Errorf("Node = %q, want manual-node", loc.Node)
	}
	if sp.selectCalls != 0 {
		t.Errorf("SelectPeer calls = %d, want 0", sp.selectCalls)
	}
}

func TestBuildLocation_TaildriveAsksShare(t *testing.T) {
	sp := &scriptedPrompter{
		strings: map[string]string{"base_path": "/v", "share": "vault"},
		backend: "taildrive",
	}
	loc, err := BuildLocation(sp, nil, "nas")
	if err != nil {
		t.Fatalf("BuildLocation: %v", err)
	}
	if loc.Backend != locations.BackendTaildrive || loc.Share != "vault" || loc.User != "" {
		t.Errorf("taildrive loc = %+v, want share=vault user empty", loc)
	}
}

func TestStdinPrompter_Scripted(t *testing.T) {
	// Simulate: pick #2, base_path default (empty line), backend ssh, user typed.
	in := strings.NewReader("2\n\nssh\nalice\n")
	var out strings.Builder
	p := NewStdinPrompter(in, &out)

	sel, err := p.SelectPeer([]Peer{{Name: "a"}, {Name: "b"}})
	if err != nil || sel.Name != "b" {
		t.Fatalf("SelectPeer = %+v, %v; want b", sel, err)
	}
	bp, err := p.AskString("base_path", "/default")
	if err != nil || bp != "/default" {
		t.Fatalf("AskString default = %q, %v; want /default", bp, err)
	}
	be, err := p.AskBackend()
	if err != nil || be != "ssh" {
		t.Fatalf("AskBackend = %q, %v; want ssh", be, err)
	}
	user, err := p.AskString("user", "")
	if err != nil || user != "alice" {
		t.Fatalf("AskString user = %q, %v; want alice", user, err)
	}
}
