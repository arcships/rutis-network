package tailscale

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/views"
)

func taggedPeers(s *ipnstate.Status, tag string) map[tailcfg.StableNodeID]netip.Addr {
	peers := make(map[tailcfg.StableNodeID]netip.Addr)
	if s == nil || s.Self == nil {
		return peers
	}
	now := time.Now()
	for _, p := range s.Peer {
		if p == nil || !p.InNetworkMap || p.ID == "" || p.ID == s.Self.ID || p.Expired ||
			p.KeyExpiry != nil && !p.KeyExpiry.IsZero() && !now.Before(*p.KeyExpiry) {
			continue
		}
		if p.Tags != nil && views.SliceContains(*p.Tags, tag) && len(p.TailscaleIPs) > 0 {
			peers[p.ID] = p.TailscaleIPs[0]
		}
	}
	return peers
}

// Recheck the selected address at the transport boundary. A cached address
// can change owners between discovery, a retry and completion of a TCP dial.
func (n *tailnet) dialTagged(ctx context.Context, id tailcfg.StableNodeID, ip netip.Addr, port uint16, tag string) (net.Conn, error) {
	address := netip.AddrPortFrom(ip, port).String()
	check := func() error {
		p, err := n.identify(ctx, address)
		if err != nil {
			return err
		}
		if p.StableID != id || !slices.Contains(p.Tags, tag) {
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

// Notifications are invalidations. Coalesce bursts and always reconcile from
// the current network map, including after a watch reconnect or missed event.
func (n *tailnet) watchChanges(ctx context.Context, changes chan<- struct{}) {
	signal := func() {
		select {
		case changes <- struct{}{}:
		default:
		}
	}
	for ctx.Err() == nil {
		w, err := n.api.WatchIPNBus(ctx, ipn.NotifyInitialState|ipn.NotifyPeerChanges|ipn.NotifyPeerPatches)
		if err == nil {
			for {
				msg, err := w.Next()
				if err != nil {
					break
				}
				if msg.State != nil || msg.SelfChange != nil || len(msg.PeersChanged)+len(msg.PeersRemoved) > 0 {
					signal()
				}
			}
			w.Close()
		}
		signal()
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

type incomingPeer struct {
	id   tailcfg.StableNodeID
	conn net.Conn
}

func (n *tailnet) acceptTagged(ctx context.Context, listener net.Listener, tag string, incoming chan<- incomingPeer) {
	defer close(incoming)
	for ctx.Err() == nil {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "link/tailscale accept:", err)
			}
			return
		}
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		peer, err := n.identify(check, conn.RemoteAddr().String())
		cancel()
		if err != nil || !slices.Contains(peer.Tags, tag) {
			conn.Close()
			fmt.Fprintln(os.Stderr, "link/tailscale listen: peer-not-allowed", conn.RemoteAddr())
			continue
		}
		select {
		case incoming <- incomingPeer{peer.StableID, conn}:
		case <-ctx.Done():
			conn.Close()
			return
		}
	}
}

type peerRun struct {
	address netip.Addr
	cancel  context.CancelFunc
	done    chan struct{}
}

func (r peerRun) stop() {
	r.cancel()
	<-r.done
}

func startPeer(ctx context.Context, ip netip.Addr, run func(context.Context)) peerRun {
	peer, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(peer)
	}()
	return peerRun{address: ip, cancel: cancel, done: done}
}

// A single owner serializes membership changes and inbound replacement.
// Each authorized stable node gets its own retry and session lifetime.
func (n *tailnet) runTagged(row context.Context, sessions tcp.Sessions, tag string, port uint16, listener net.Listener) {
	ctx, cancel := context.WithCancel(row)
	changes := make(chan struct{}, 1)
	changes <- struct{}{}
	var background sync.WaitGroup
	background.Go(func() { n.watchChanges(ctx, changes) })
	var incoming chan incomingPeer
	if listener != nil {
		incoming = make(chan incomingPeer)
		background.Go(func() { n.acceptTagged(ctx, listener, tag, incoming) })
	}
	active := make(map[tailcfg.StableNodeID]peerRun)
	defer func() {
		cancel()
		if listener != nil {
			listener.Close()
		}
		for _, run := range active {
			run.stop()
		}
		background.Wait()
	}()
	var allowed map[tailcfg.StableNodeID]netip.Addr
	refresh := func() {
		check, end := context.WithTimeout(ctx, 5*time.Second)
		s, err := n.status(check)
		end()
		if err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "link/tailscale discovery:", err)
		}
		allowed = taggedPeers(s, tag)
		for id, run := range active {
			if ip, ok := allowed[id]; !ok || run.address != ip {
				run.stop()
				delete(active, id)
			}
		}
		if listener != nil {
			return
		}
		for id, ip := range allowed {
			if _, ok := active[id]; ok {
				continue
			}
			active[id] = startPeer(ctx, ip, func(peerCtx context.Context) {
				n.dialLoop(peerCtx, row, sessions, ip.String(), func(attempt context.Context) (net.Conn, error) {
					return n.dialTagged(attempt, id, ip, port, tag)
				})
			})
		}
	}
	// Also reconcile expiry and support system daemons without peer-change notifications.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			refresh()
		case <-ticker.C:
			refresh()
		case peer, ok := <-incoming:
			if !ok {
				return
			}
			ip, ok := allowed[peer.id]
			if !ok {
				peer.conn.Close()
				continue
			}
			if previous, ok := active[peer.id]; ok {
				previous.stop()
			}
			active[peer.id] = startPeer(ctx, ip, func(peerCtx context.Context) {
				if err := n.peerSession(peerCtx, row, sessions, peer.conn, ip.String()); err != nil && peerCtx.Err() == nil {
					fmt.Fprintln(os.Stderr, "link/tailscale session:", err)
				}
			})
		}
	}
}
