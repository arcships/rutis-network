package libp2p

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/arcships/rutis/go/rutis"
	p2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	ma "github.com/multiformats/go-multiaddr"
)

const protocolID = "/rutis/link/1"

var Plugin = rutis.Define(rutis.Plugin[config]{
	Name:   "link-libp2p",
	Inject: []string{"rutis/sessions"},
	Schema: rowSchema(),
	Apply: func(row *rutis.Ctx, cfg config) error {
		opts, err := cfg.parse()
		if err != nil {
			return err
		}
		c := &control{row: row, opts: opts}
		if err := row.Use("rutis/sessions", &c.service); err != nil {
			return err
		}
		row.Effect(func(context.Context) error { return c.Stop() })
		if cfg.ControlService != "" {
			row.Provide(cfg.ControlService, c)
		}
		return c.Start()
	},
})

type snapshot struct {
	Phase       string   `json:"phase"`
	LocalPeer   string   `json:"localPeer,omitempty"`
	ListenAddrs []string `json:"listenAddrs,omitempty"`
	Peer        string   `json:"peer,omitempty"`
	Path        string   `json:"path,omitempty"`
	LastError   string   `json:"lastError,omitempty"`
}

type control struct {
	row      context.Context
	opts     settings
	service  sessions
	commands sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	mu       sync.Mutex
	state    snapshot
}

func (c *control) Status() snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.state
	s.ListenAddrs = slices.Clone(s.ListenAddrs)
	return s
}

func (c *control) Start() error {
	c.commands.Lock()
	defer c.commands.Unlock()
	if err := c.row.Err(); err != nil {
		return err
	}
	if c.done != nil {
		select {
		case <-c.done:
		default:
			return nil
		}
	}
	ctx, cancel := context.WithCancel(c.row)
	c.cancel = cancel
	c.done = make(chan struct{})
	c.mu.Lock()
	c.state = snapshot{Phase: "starting"}
	c.mu.Unlock()
	go func() {
		defer close(c.done)
		defer cancel()
		err := c.run(ctx)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.state = snapshot{Phase: "disconnected"}
		if err != nil && ctx.Err() == nil {
			c.state.Phase = "failed"
			c.state.LastError = err.Error()
			fmt.Fprintln(os.Stderr, "link/libp2p:", err)
		}
	}()
	return nil
}

func (c *control) Stop() error {
	c.commands.Lock()
	defer c.commands.Unlock()
	if c.done != nil {
		c.cancel()
		<-c.done
		c.done = nil
	}
	c.mu.Lock()
	c.state = snapshot{Phase: "stopped"}
	c.mu.Unlock()
	return nil
}

func (c *control) run(ctx context.Context) error {
	opts := []p2p.Option{p2p.Identity(c.opts.key), p2p.NoTransports, p2p.Transport(quic.NewTransport), p2p.NoListenAddrs}
	if c.opts.listen != nil {
		opts = append(opts, p2p.ListenAddrs(c.opts.listen))
	}
	if c.opts.relay != nil || c.opts.circuit {
		opts = append(opts, p2p.EnableRelay())
	} else {
		opts = append(opts, p2p.DisableRelay())
	}
	if c.opts.relay != nil {
		// AutoRelay acquires and renews the reservation for the row's lifetime.
		opts = append(opts, p2p.ForceReachabilityPrivate(), p2p.EnableAutoRelayWithStaticRelays([]peer.AddrInfo{*c.opts.relay}))
	}
	h, err := p2p.New(opts...)
	if err != nil {
		return err
	}
	defer h.Close()
	c.mu.Lock()
	c.state.LocalPeer = h.ID().String()
	for _, a := range h.Addrs() {
		c.state.ListenAddrs = append(c.state.ListenAddrs, a.String())
	}
	c.mu.Unlock()
	if c.opts.listen != nil {
		return c.listen(ctx, h)
	}
	c.mu.Lock()
	c.state.Phase = "dialing"
	c.mu.Unlock()
	dialing, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if c.opts.circuit {
		dialing = network.WithAllowLimitedConn(dialing, "configured circuit address")
	}
	if err := h.Connect(dialing, *c.opts.dial); err != nil {
		return err
	}
	stream, err := h.NewStream(dialing, c.opts.dial.ID, protocolID)
	if err != nil {
		return err
	}
	cancel()
	return c.attach(ctx, stream)
}

func (c *control) attach(ctx context.Context, stream network.Stream) error {
	return bridge(ctx, c.row, stream, c.service, func() {
		path := "direct"
		if _, err := stream.Conn().RemoteMultiaddr().ValueForProtocol(ma.P_CIRCUIT); err == nil {
			path = "relay"
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.state.Phase = "connected"
		c.state.Peer = stream.Conn().RemotePeer().String()
		c.state.Path = path
		c.state.LastError = ""
	})
}

func (c *control) listen(ctx context.Context, h host.Host) error {
	// The token covers admission, session open and cleanup. A racing second
	// stream cannot replace the active one, including during shutdown.
	slot := make(chan struct{}, 1)
	slot <- struct{}{}
	c.mu.Lock()
	h.SetStreamHandler(protocolID, func(s network.Stream) {
		if s.Conn().RemotePeer() != c.opts.allowed {
			_ = s.ResetWithError(network.StreamGated)
			return
		}
		select {
		case <-slot:
			defer func() { slot <- struct{}{} }()
		default:
			_ = s.ResetWithError(network.StreamResourceLimitExceeded)
			return
		}
		if ctx.Err() != nil {
			_ = s.Reset()
			return
		}
		err := c.attach(ctx, s)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.state.Phase, c.state.Peer, c.state.Path = "listening", "", ""
		if err != nil && !errors.Is(err, context.Canceled) {
			c.state.LastError = err.Error()
			fmt.Fprintln(os.Stderr, "link/libp2p:", err)
		}
	})
	c.state.Phase = "listening"
	c.mu.Unlock()
	<-ctx.Done()
	h.RemoveStreamHandler(protocolID)
	<-slot
	return ctx.Err()
}
