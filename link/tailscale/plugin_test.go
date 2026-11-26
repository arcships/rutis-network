package tailscale

import (
	"net/netip"
	"testing"

	"tailscale.com/ipn/ipnstate"
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
