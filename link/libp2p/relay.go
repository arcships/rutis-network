package libp2p

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
)

// Reserve the configured relay directly so private relays work too. AutoRelay
// only advertises public relay addresses, even after it obtains a reservation.
func (c *control) maintainRelay(ctx context.Context, h host.Host) {
	relay := *c.opts.relay
	disconnected := make(chan struct{}, 1)
	notify := &network.NotifyBundle{DisconnectedF: func(n network.Network, conn network.Conn) {
		if conn.RemotePeer() == relay.ID && n.Connectedness(relay.ID) != network.Connected {
			select {
			case disconnected <- struct{}{}:
			default:
			}
		}
	}}
	h.Network().Notify(notify)
	defer h.Network().StopNotify(notify)
	h.ConnManager().Protect(relay.ID, "rutis-relay")
	defer h.ConnManager().Unprotect(relay.ID, "rutis-relay")

	timer := time.NewTimer(0)
	defer timer.Stop()
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-disconnected:
			c.mu.Lock()
			c.relayExpires = time.Time{}
			c.mu.Unlock()
		case <-timer.C:
		}
		r, err := client.Reserve(ctx, h, relay)
		delay := 5 * time.Second
		c.mu.Lock()
		if err != nil {
			c.relayExpires = time.Time{}
		} else {
			c.relayExpires = r.Expiration
			delay = time.Until(r.Expiration) / 2
		}
		c.mu.Unlock()
		timer.Reset(delay)
	}
}
