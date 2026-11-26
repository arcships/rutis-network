package tailscale

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/views"
)

func TestEndpoints(t *testing.T) {
	for _, cfg := range []config{
		{Dial: "node:7443"}, {Dial: "[fd7a:115c:a1e0::1]:7443"}, {Listen: ":0", AllowPeer: "node.example.ts.net."},
	} {
		if _, _, err := cfg.endpoint(); err != nil {
			t.Fatalf("%+v: %v", cfg, err)
		}
	}
	for _, cfg := range []config{
		{}, {Dial: "node:0"}, {Dial: ":7443"}, {Dial: "node:80", Listen: ":80"}, {Listen: ":1"}, {Listen: ":1", AllowPeer: "*"},
		{Dial: "node:80", AllowPeer: "node"}, {Dial: "bad/name:80"}, {Dial: "node:80", TSNet: &embeddedNode{Hostname: "node", StateDir: "relative"}},
	} {
		if _, _, err := cfg.endpoint(); err == nil {
			t.Errorf("accepted invalid config %+v", cfg)
		}
	}
}

func TestTailnetNames(t *testing.T) {
	ip := netip.MustParseAddr("100.64.0.10")
	v6 := netip.MustParseAddr("fd7a:115c:a1e0::10")
	s := &ipnstate.Status{Self: &ipnstate.PeerStatus{DNSName: "work.example.ts.net.", TailscaleIPs: []netip.Addr{ip, v6}}, CurrentTailnet: &ipnstate.TailnetStatus{MagicDNSSuffix: "example.ts.net"}}
	for target, want := range map[string]netip.Addr{"work": ip, "Work.Example.ts.net.": ip, v6.String(): v6, "100.64.0.20": {}, "work.other.ts.net": {}, "127.0.0.1": {}} {
		_, got := findPeer(s, target)
		if got != want {
			t.Errorf("%s: got %v, want %v", target, got, want)
		}
	}
}

func TestDiscoveryEndpoints(t *testing.T) {
	for _, tc := range []struct {
		cfg  config
		port uint16
	}{
		{config{Discover: &discovery{Tag: "tag:worker"}}, defaultPort},
		{config{Discover: &discovery{Tag: "tag:worker", Port: 8333}}, 8333},
		{config{AllowTag: "tag:controller"}, defaultPort},
		{config{Listen: ":8333", AllowTag: "tag:controller"}, 8333},
	} {
		host, port, err := tc.cfg.endpoint()
		if err != nil || host != "" || port != tc.port {
			t.Errorf("%+v: got %q:%d, %v", tc.cfg, host, port, err)
		}
	}
	discover := &discovery{Tag: "tag:worker"}
	for _, cfg := range []config{
		{Discover: &discovery{}}, {Discover: &discovery{Tag: "tag:*"}}, {AllowTag: "*"},
		{Discover: discover, Dial: "node:80"}, {Discover: discover, Listen: ":80"},
		{Discover: discover, AllowPeer: "node"}, {Discover: discover, AllowTag: "tag:controller"},
		{AllowTag: "tag:controller", Dial: "node:80"}, {AllowTag: "tag:controller", AllowPeer: "node"},
	} {
		if _, _, err := cfg.endpoint(); err == nil {
			t.Errorf("accepted invalid configuration %+v", cfg)
		}
	}
}

func TestTaggedPeers(t *testing.T) {
	const tag = "tag:worker"
	ip := netip.MustParseAddr("100.64.0.10")
	tags := views.SliceOf([]string{"tag:second", tag})
	p := ipnstate.PeerStatus{ID: "worker", TailscaleIPs: []netip.Addr{ip}, InNetworkMap: true, Tags: &tags}
	s := &ipnstate.Status{Self: &ipnstate.PeerStatus{ID: "self"}, Peer: map[key.NodePublic]*ipnstate.PeerStatus{{}: &p}}
	if got := taggedPeers(s, tag); len(got) != 1 || got[p.ID] != ip {
		t.Fatalf("tagged worker missing: %v", got)
	}
	for _, change := range []func(*ipnstate.PeerStatus){
		func(p *ipnstate.PeerStatus) { p.InNetworkMap = false },
		func(p *ipnstate.PeerStatus) { p.ID = "self" },
		func(p *ipnstate.PeerStatus) { p.ID = "" },
		func(p *ipnstate.PeerStatus) { p.Tags = nil },
		func(p *ipnstate.PeerStatus) { p.Expired = true },
		func(p *ipnstate.PeerStatus) { expired := time.Now().Add(-time.Second); p.KeyExpiry = &expired },
		func(p *ipnstate.PeerStatus) { p.TailscaleIPs = nil },
	} {
		peer := p
		change(&peer)
		s.Peer[key.NodePublic{}] = &peer
		if got := taggedPeers(s, tag); len(got) != 0 {
			t.Errorf("unexpected discovered peer %v", got)
		}
	}
}

func TestDialRejectsReassignedAddress(t *testing.T) {
	ip := netip.MustParseAddr("100.64.0.10")
	const id, tag = "original", "tag:worker"
	// The same tagged address belongs to a different node when the TCP dial finishes.
	responses := make(chan tailcfg.StableNodeID, 2)
	responses <- id
	responses <- "replacement"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/localapi/v0/whois" {
			http.NotFound(w, r)
			return
		}
		p := &tailcfg.Node{StableID: <-responses, Tags: []string{tag}, Addresses: []netip.Prefix{netip.PrefixFrom(ip, 32)}}
		json.NewEncoder(w).Encode(apitype.WhoIsResponse{Node: p})
	}))
	defer server.Close()
	client := &local.Client{OmitAuth: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	first, second := net.Pipe()
	defer first.Close()
	defer second.Close()
	n := &tailnet{api: client, dialIP: func(context.Context, string, uint16) (net.Conn, error) { return first, nil }}
	if conn, err := n.dialTagged(t.Context(), id, ip, defaultPort, tag); err == nil || conn != nil {
		t.Fatal("accepted an address reassigned to a different tagged node")
	}
	if len(responses) != 0 {
		t.Fatal("expected WhoIs both before and after dialing")
	}
	second.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := second.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("rejected stream was not closed: %v", err)
	}
}
