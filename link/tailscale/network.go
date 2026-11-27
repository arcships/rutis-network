// Package tailscale mounts a TCP stream from an admitted tailnet node as a rutis session.
package tailscale

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

var errKeyExpired = errors.New("node-key-expired")

// System rows share the daemon's LocalAPI connection pool across reloads.
var systemAPI = new(local.Client)

type tailnet struct {
	api    *local.Client
	dialIP func(context.Context, string, uint16) (net.Conn, error)
	listen func(context.Context, string, string) (net.Listener, error)
	close  func() error
}

func openNetwork(ctx context.Context, opts *embeddedNode) (*tailnet, error) {
	if opts == nil {
		var lc net.ListenConfig
		return &tailnet{api: systemAPI, dialIP: systemAPI.DialTCP, listen: lc.Listen}, nil
	}
	server := &tsnet.Server{Dir: opts.StateDir, Hostname: opts.Hostname}
	if _, err := server.Up(ctx); err != nil {
		server.Close()
		return nil, fmt.Errorf("tsnet-start: %w", err)
	}
	api, err := server.LocalClient()
	if err != nil {
		server.Close()
		return nil, err
	}
	return &tailnet{api: api, close: server.Close, dialIP: func(ctx context.Context, ip string, port uint16) (net.Conn, error) {
		return server.Dial(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(int(port))))
	}, listen: func(_ context.Context, network, address string) (net.Listener, error) {
		return server.Listen(network, address)
	}}, nil
}

func (n *tailnet) status(ctx context.Context) (*ipnstate.Status, error) {
	s, err := n.api.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("tailscaled-unavailable: %w", err)
	}
	if s.BackendState != "Running" || s.Self == nil || len(s.TailscaleIPs) == 0 {
		return nil, fmt.Errorf("tailnet-not-ready: %s", s.BackendState)
	}
	if s.Self.Expired {
		return nil, errKeyExpired
	}
	return s, nil
}

// Resolve only node addresses from the daemon's network map. Subnet routes,
// exit-node routes and public DNS addresses cannot select a peer.
func nodeAddress(target string, node *ipnstate.PeerStatus, suffix string) (netip.Addr, bool) {
	if node == nil || len(node.TailscaleIPs) == 0 {
		return netip.Addr{}, false
	}
	if ip, err := netip.ParseAddr(target); err == nil {
		return ip, slices.Contains(node.TailscaleIPs, ip)
	}
	name := strings.TrimSuffix(target, ".")
	if !strings.Contains(name, ".") && suffix != "" {
		name += "." + strings.Trim(suffix, ".")
	}
	return node.TailscaleIPs[0], strings.EqualFold(name, strings.TrimSuffix(node.DNSName, "."))
}

func findPeer(s *ipnstate.Status, target string) (*ipnstate.PeerStatus, netip.Addr) {
	suffix := s.MagicDNSSuffix
	if s.CurrentTailnet != nil {
		suffix = s.CurrentTailnet.MagicDNSSuffix
	}
	if ip, ok := nodeAddress(target, s.Self, suffix); ok {
		return s.Self, ip
	}
	for _, p := range s.Peer {
		if p == nil || !p.InNetworkMap {
			continue
		}
		if ip, ok := nodeAddress(target, p, suffix); ok {
			return p, ip
		}
	}
	return nil, netip.Addr{}
}

func (n *tailnet) dial(ctx context.Context, target string, port uint16) (net.Conn, error) {
	s, err := n.status(ctx)
	if err != nil {
		return nil, err
	}
	node, ip := findPeer(s, target)
	if node == nil {
		return nil, fmt.Errorf("peer-not-found: %s", target)
	}
	if node.Expired {
		return nil, errKeyExpired
	}
	ips := node.TailscaleIPs
	if _, err := netip.ParseAddr(target); err == nil {
		ips = []netip.Addr{ip}
	}
	return n.dialPeer(ctx, node.ID, ips, port, "")
}

// Divide the remaining attempt time between addresses so a silent first
// address cannot consume the entire deadline before the other family is tried.
func (n *tailnet) dialPeer(ctx context.Context, id tailcfg.StableNodeID, ips []netip.Addr, port uint16, tag string) (net.Conn, error) {
	if len(ips) == 0 {
		return nil, errors.New("peer has no tailnet addresses")
	}
	var failures []error
	for i, ip := range ips {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		budget := 10 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			budget = time.Until(deadline) / time.Duration(len(ips)-i)
		}
		attempt, cancel := context.WithTimeout(ctx, budget)
		conn, err := n.dialVerified(attempt, id, ip, port, tag)
		cancel()
		if err == nil {
			return conn, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", ip, err))
	}
	return nil, errors.Join(failures...)
}

// Check the selected address on both sides of the dial. Reassignment or tag
// revocation while connecting must be observed before the stream is mounted.
func (n *tailnet) dialVerified(ctx context.Context, id tailcfg.StableNodeID, ip netip.Addr, port uint16, tag string) (net.Conn, error) {
	addr := net.TCPAddrFromAddrPort(netip.AddrPortFrom(ip, port))
	check := func() error {
		p, err := n.identify(ctx, addr)
		if err != nil {
			return err
		}
		if p.StableID != id || tag != "" && !slices.Contains(p.Tags, tag) {
			return errors.New("peer-not-allowed")
		}
		return nil
	}
	if err := check(); err != nil {
		return nil, err
	}
	conn, err := n.dialIP(ctx, ip.String(), port)
	if err != nil {
		return nil, fmt.Errorf("tailnet-dial: %w", err)
	}
	if err := check(); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (n *tailnet) identify(ctx context.Context, remote net.Addr) (*tailcfg.Node, error) {
	if remote == nil {
		return nil, errors.New("unknown-peer: missing remote address")
	}
	addr := remote.String()
	source, err := netip.ParseAddrPort(addr)
	if err != nil {
		return nil, fmt.Errorf("unknown-peer: %w", err)
	}
	w, err := n.api.WhoIsProto(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("unknown-peer: %w", err)
	}
	if w == nil || w.Node == nil || w.Node.StableID == "" || w.Node.Expired || !w.Node.KeyExpiry.IsZero() && !time.Now().Before(w.Node.KeyExpiry) {
		return nil, errors.New("invalid-peer-identity")
	}
	ip := source.Addr().Unmap()
	if !slices.Contains(w.Node.Addresses, netip.PrefixFrom(ip, ip.BitLen())) {
		return nil, errors.New("source-is-not-node-address")
	}
	return w.Node, nil
}

func (n *tailnet) admit(ctx context.Context, addr net.Addr, allowed string) error {
	peer, err := n.identify(ctx, addr)
	if err != nil {
		return err
	}
	s, err := n.status(ctx)
	if err != nil {
		return err
	}
	node, _ := findPeer(s, allowed)
	if node == nil || node.Expired || node.ID != peer.StableID {
		return errors.New("peer-not-allowed")
	}
	return nil
}

func (n *tailnet) path(ctx context.Context, target string) string {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	s, err := n.status(ctx)
	if err != nil {
		return "unknown"
	}
	p, _ := findPeer(s, target)
	if p == nil {
		for _, node := range s.Peer {
			if node != nil && string(node.ID) == target {
				p = node
				break
			}
		}
	}
	switch {
	case p == s.Self:
		return "local"
	case p == nil || !p.Active:
		return "unknown"
	case p.CurAddr != "":
		return "direct"
	case p.PeerRelay != "":
		return "peer-relay"
	case p.Relay != "":
		return "DERP"
	default:
		return "unknown"
	}
}
