package tailscale

import (
	"context"
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

func taggedPeers(s *ipnstate.Status, tag string) map[tailcfg.StableNodeID][]netip.Addr {
	peers := make(map[tailcfg.StableNodeID][]netip.Addr)
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
			peers[p.ID] = p.TailscaleIPs
		}
	}
	return peers
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
		addr := conn.RemoteAddr()
		peer, err := n.identify(check, addr)
		cancel()
		if err != nil || !slices.Contains(peer.Tags, tag) {
			conn.Close()
			fmt.Fprintln(os.Stderr, "link/tailscale listen: peer-not-allowed", addr)
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
	addresses []netip.Addr
	cancel    context.CancelFunc
	// Keep only the newest admitted connection while the previous session ends.
	pending net.Conn
}

func (r *peerRun) discardPending() {
	if r.pending != nil {
		r.pending.Close()
		r.pending = nil
	}
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
	active := make(map[tailcfg.StableNodeID]*peerRun)
	finished := make(chan tailcfg.StableNodeID)
	defer func() {
		cancel()
		if listener != nil {
			listener.Close()
		}
		for _, run := range active {
			run.discardPending()
		}
		background.Wait()
	}()
	start := func(id tailcfg.StableNodeID, ips []netip.Addr, conn net.Conn) {
		peerCtx, stop := context.WithCancel(ctx)
		active[id] = &peerRun{addresses: ips, cancel: stop}
		background.Go(func() {
			defer stop()
			if conn == nil {
				n.dialLoop(peerCtx, row, sessions, string(id), func(attempt context.Context) (net.Conn, error) {
					return n.dialPeer(attempt, id, ips, port, tag)
				})
			} else if err := n.peerSession(peerCtx, row, sessions, conn, string(id)); err != nil && peerCtx.Err() == nil {
				fmt.Fprintln(os.Stderr, "link/tailscale session:", err)
			}
			select {
			case finished <- id:
			case <-ctx.Done():
			}
		})
	}
	var allowed map[tailcfg.StableNodeID][]netip.Addr
	refresh := func() {
		check, end := context.WithTimeout(ctx, 5*time.Second)
		s, err := n.status(check)
		end()
		if err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "link/tailscale discovery:", err)
		}
		allowed = taggedPeers(s, tag)
		for id, run := range active {
			if !slices.Equal(allowed[id], run.addresses) {
				run.cancel()
				run.discardPending()
			}
		}
		if listener != nil {
			return
		}
		for id, ips := range allowed {
			if _, ok := active[id]; ok {
				continue
			}
			start(id, ips, nil)
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
		case id := <-finished:
			run := active[id]
			delete(active, id)
			if ips := allowed[id]; len(ips) > 0 && (listener == nil || run.pending != nil) {
				start(id, ips, run.pending)
			} else {
				run.discardPending()
			}
		case peer, ok := <-incoming:
			if !ok {
				return
			}
			ips, ok := allowed[peer.id]
			if !ok {
				peer.conn.Close()
				continue
			}
			if previous := active[peer.id]; previous != nil {
				previous.cancel()
				previous.discardPending()
				previous.pending = peer.conn
				continue
			}
			start(peer.id, ips, peer.conn)
		}
	}
}
