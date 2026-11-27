package tailscale

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/net/netutil"
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
	ips := []netip.Addr{ip, netip.MustParseAddr("fd7a:115c:a1e0::10")}
	p := ipnstate.PeerStatus{ID: "worker", TailscaleIPs: ips, InNetworkMap: true, Tags: &tags}
	s := &ipnstate.Status{Self: &ipnstate.PeerStatus{ID: "self"}, Peer: map[key.NodePublic]*ipnstate.PeerStatus{{}: &p}}
	if got := taggedPeers(s, tag); len(got) != 1 || !slices.Equal(got[p.ID], ips) {
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

func testLocalAPI(t *testing.T, handler http.HandlerFunc) *local.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &local.Client{OmitAuth: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
}

func serveTestStatus(w http.ResponseWriter, leaf *ipnstate.PeerStatus) {
	json.NewEncoder(w).Encode(ipnstate.Status{
		BackendState: "Running",
		Self:         &ipnstate.PeerStatus{ID: "host"},
		TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")},
		Peer:         map[key.NodePublic]*ipnstate.PeerStatus{{}: leaf},
	})
}

func serveTestNotification(w http.ResponseWriter, r *http.Request, trigger <-chan struct{}, event ipn.Notify) {
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	select {
	case <-trigger:
		json.NewEncoder(w).Encode(event)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	case <-r.Context().Done():
	}
}

type connWithoutRemoteAddr struct{ net.Conn }

func (connWithoutRemoteAddr) RemoteAddr() net.Addr { return nil }

func TestMissingRemoteAddress(t *testing.T) {
	for _, mode := range []string{"allowTag", "allowPeer"} {
		t.Run(mode, func(t *testing.T) {
			conn, other := net.Pipe()
			defer conn.Close()
			defer other.Close()
			listener := netutil.NewOneConnListener(connWithoutRemoteAddr{conn}, nil)
			defer listener.Close()
			n := new(tailnet)
			if mode == "allowTag" {
				incoming := make(chan incomingPeer, 1)
				n.acceptTagged(t.Context(), listener, "tag:host", incoming)
				if _, admitted := <-incoming; admitted {
					t.Fatal("admitted a missing remote address")
				}
			} else {
				n.accept(t.Context(), tcp.Sessions{}, listener, "host")
			}
			other.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := other.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("rejected connection still open: %v", err)
			}
		})
	}
}

func TestDialRejectsReassignedAddress(t *testing.T) {
	ip := netip.MustParseAddr("100.64.0.10")
	const id, tag = "original", "tag:worker"
	for _, mode := range []string{"tag", "MagicDNS"} {
		t.Run(mode, func(t *testing.T) {
			var dialed atomic.Bool
			client := testLocalAPI(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/localapi/v0/status":
					node := &ipnstate.PeerStatus{ID: id, DNSName: "leaf.test.ts.net.", TailscaleIPs: []netip.Addr{ip}, InNetworkMap: true}
					serveTestStatus(w, node)
				case "/localapi/v0/whois":
					p := &tailcfg.Node{StableID: id, Tags: []string{tag}, Addresses: []netip.Prefix{netip.PrefixFrom(ip, 32)}}
					if dialed.Load() {
						p.StableID = "replacement"
					}
					json.NewEncoder(w).Encode(apitype.WhoIsResponse{Node: p})
				default:
					http.NotFound(w, r)
				}
			})
			first, second := net.Pipe()
			defer first.Close()
			defer second.Close()
			n := &tailnet{api: client, dialIP: func(context.Context, string, uint16) (net.Conn, error) {
				dialed.Store(true)
				return first, nil
			}}
			var conn net.Conn
			var err error
			if mode == "MagicDNS" {
				conn, err = n.dial(t.Context(), "leaf.test.ts.net", defaultPort)
			} else {
				conn, err = n.dialPeer(t.Context(), id, []netip.Addr{ip}, defaultPort, tag)
			}
			if !dialed.Load() || err == nil || conn != nil {
				t.Fatalf("dialed=%t, conn=%v, err=%v", dialed.Load(), conn, err)
			}
			second.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := second.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("rejected stream was not closed: %v", err)
			}
		})
	}
}

func TestMagicDNSFallsBackAfterAddressTimeout(t *testing.T) {
	ips := []netip.Addr{netip.MustParseAddr("100.64.0.2"), netip.MustParseAddr("fd7a:115c:a1e0::2")}
	leaf := &ipnstate.PeerStatus{ID: "leaf", DNSName: "leaf.test.ts.net.", InNetworkMap: true, TailscaleIPs: ips}
	client := testLocalAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/localapi/v0/status":
			serveTestStatus(w, leaf)
		case "/localapi/v0/whois":
			p := &tailcfg.Node{StableID: leaf.ID, Addresses: []netip.Prefix{netip.PrefixFrom(ips[0], 32), netip.PrefixFrom(ips[1], 128)}}
			json.NewEncoder(w).Encode(apitype.WhoIsResponse{Node: p})
		default:
			http.NotFound(w, r)
		}
	})
	var attempts []string
	conn, other := net.Pipe()
	defer other.Close()
	defer conn.Close()
	n := &tailnet{api: client, dialIP: func(ctx context.Context, host string, _ uint16) (net.Conn, error) {
		attempts = append(attempts, host)
		if host == ips[0].String() {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return conn, nil
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	got, err := n.dial(ctx, leaf.DNSName, defaultPort)
	if err != nil || got != conn || !slices.Equal(attempts, []string{ips[0].String(), ips[1].String()}) {
		t.Fatalf("attempts=%v, conn=%v, err=%v", attempts, got, err)
	}
}

// Switch the visible tag from leaf A to B. A's receive must be interrupted,
// and B must connect even while the host is still acknowledging A's near.end.
func TestDiscoveryIsolatesPeerShutdown(t *testing.T) {
	const tag = "tag:worker"
	ipA := netip.MustParseAddr("100.64.0.2")
	ipB := netip.MustParseAddr("100.64.0.3")
	tags := views.SliceOf([]string{tag})
	changed := make(chan struct{})
	client := testLocalAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/localapi/v0/watch-ipn-bus":
			serveTestNotification(w, r, changed, ipn.Notify{PeersRemoved: []tailcfg.NodeID{1}})
		case "/localapi/v0/status":
			leaf := &ipnstate.PeerStatus{ID: "leaf-a", InNetworkMap: true, Tags: &tags, TailscaleIPs: []netip.Addr{ipA}}
			select {
			case <-changed:
				leaf.ID = "leaf-b"
				leaf.TailscaleIPs = []netip.Addr{ipB}
			default:
			}
			serveTestStatus(w, leaf)
		case "/localapi/v0/whois":
			endpoint, err := netip.ParseAddrPort(r.URL.Query().Get("addr"))
			if err != nil {
				http.Error(w, "invalid address", http.StatusBadRequest)
				return
			}
			p := &tailcfg.Node{StableID: "leaf-a", Tags: []string{tag}, Addresses: []netip.Prefix{netip.PrefixFrom(endpoint.Addr(), 32)}}
			if endpoint.Addr() == ipB {
				p.StableID = "leaf-b"
			}
			json.NewEncoder(w).Encode(apitype.WhoIsResponse{Node: p})
		default:
			http.NotFound(w, r)
		}
	})
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	connA, otherA := net.Pipe()
	defer otherA.Close()
	connB, otherB := net.Pipe()
	defer otherB.Close()
	n := &tailnet{api: client, dialIP: func(_ context.Context, host string, _ uint16) (net.Conn, error) {
		if host == ipA.String() {
			return connA, nil
		}
		return connB, nil
	}}
	receivingA, endingA, receivingB := make(chan struct{}), make(chan error, 1), make(chan struct{})
	release := make(chan struct{})
	sessions := tcp.Sessions{Open: func(context.Context, tcp.Far) (tcp.Near, error) {
		var name string
		return tcp.Near{
			Receive: func(ctx context.Context, frame []byte) error {
				name = string(frame)
				if name == "A" {
					close(receivingA)
				} else {
					close(receivingB)
				}
				<-ctx.Done()
				return ctx.Err()
			},
			End: func(ctx context.Context, _ tcp.CloseReason) error {
				if name == "A" {
					endingA <- ctx.Err()
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
				return nil
			},
		}, nil
	}}
	var workers sync.WaitGroup
	workers.Go(func() { n.runTagged(ctx, sessions, tag, defaultPort, nil) })
	defer func() { close(release); stop(); workers.Wait() }()
	send := func(conn net.Conn, id byte, received <-chan struct{}) {
		t.Helper()
		deadline, _ := ctx.Deadline()
		conn.SetWriteDeadline(deadline)
		if _, err := conn.Write([]byte{0, 0, 0, 1, id}); err != nil {
			t.Fatalf("leaf %c: %v", id, err)
		}
		select {
		case <-received:
		case <-ctx.Done():
			t.Fatalf("leaf %c did not receive", id)
		}
	}
	send(otherA, 'A', receivingA)
	close(changed)
	select {
	case err := <-endingA:
		if err != nil {
			t.Fatalf("lost row context for near.end: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("revocation did not interrupt A's receive")
	}
	send(otherB, 'B', receivingB)
}
