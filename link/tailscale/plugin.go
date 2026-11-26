package tailscale

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"github.com/arcships/rutis/go/rutis"
	"tailscale.com/util/dnsname"
)

type embeddedNode struct {
	Hostname string `json:"hostname"`
	StateDir string `json:"stateDir"`
}

type config struct {
	Dial      string        `json:"dial,omitempty"`
	Listen    string        `json:"listen,omitempty"`
	AllowPeer string        `json:"allowPeer,omitempty"`
	TSNet     *embeddedNode `json:"tsnet,omitempty"`
}

func validPeer(value string) bool {
	if ip, err := netip.ParseAddr(value); err == nil {
		return ip.Zone() == "" && !ip.IsUnspecified()
	}
	return value != "" && dnsname.ValidHostname(value) == nil
}

func (c config) endpoint() (host string, port uint16, err error) {
	if c.TSNet != nil && (dnsname.ValidLabel(c.TSNet.Hostname) != nil || !filepath.IsAbs(c.TSNet.StateDir)) {
		return "", 0, errors.New("tsnet requires hostname and an absolute stateDir unique to this row")
	}
	if (c.Dial == "") == (c.Listen == "") {
		return "", 0, errors.New("set exactly one of dial or listen")
	}
	address := c.Dial
	if c.Listen != "" {
		if !validPeer(c.AllowPeer) {
			return "", 0, errors.New("listen requires a node IP or MagicDNS name in allowPeer")
		}
		address = c.Listen
	} else if c.AllowPeer != "" {
		return "", 0, errors.New("allowPeer is a listener setting")
	}
	host, raw, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, err
	}
	p, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || c.Dial != "" && (p == 0 || !validPeer(host)) {
		return "", 0, errors.New("invalid endpoint: use host:port for dial, or [tailnet-IP]:port for listen")
	}
	return host, uint16(p), nil
}

var Plugin = rutis.Define(rutis.Plugin[config]{
	Name:   "link-tailscale",
	Inject: []string{"rutis/sessions"},
	Apply: func(row *rutis.Ctx, cfg config) error {
		host, port, err := cfg.endpoint()
		if err != nil {
			return err
		}
		var sessions tcp.Sessions
		if err := row.Use("rutis/sessions", &sessions); err != nil {
			return err
		}
		startup, endStartup := context.WithTimeout(row, 30*time.Second)
		defer endStartup()
		n, err := openNetwork(startup, cfg.TSNet)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithCancel(row)
		var workers sync.WaitGroup
		row.Effect(func(context.Context) error {
			cancel()
			workers.Wait()
			if n.close != nil {
				return n.close()
			}
			return nil
		})
		s, err := n.status(startup)
		if err != nil {
			return err
		}
		if cfg.Dial != "" {
			workers.Go(func() { n.dialLoop(ctx, sessions, host, port) })
			return nil
		}
		ip := s.TailscaleIPs[0]
		if host != "" {
			ip, err = netip.ParseAddr(host)
			if err != nil || !slices.Contains(s.TailscaleIPs, ip) {
				return errors.New("listen address must belong to this Tailscale node")
			}
		}
		listener, err := n.listen(startup, "tcp", netip.AddrPortFrom(ip, port).String())
		if err != nil {
			return fmt.Errorf("tailnet-listen: %w", err)
		}
		fmt.Fprintln(os.Stderr, "link/tailscale listening", listener.Addr())
		workers.Go(func() { n.accept(ctx, sessions, listener, cfg.AllowPeer) })
		return nil
	},
})

func (n *tailnet) dialLoop(ctx context.Context, sessions tcp.Sessions, host string, port uint16) {
	delay := 500 * time.Millisecond
	for ctx.Err() == nil {
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		conn, err := n.dial(attempt, host, port)
		cancel()
		if err == nil {
			start := time.Now()
			err = n.session(ctx, sessions, conn, host)
			if time.Since(start) >= time.Minute {
				delay = 500 * time.Millisecond
			}
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errKeyExpired) {
			delay = 30 * time.Second
		}
		if err == nil {
			err = errors.New("connection closed")
		}
		fmt.Fprintf(os.Stderr, "link/tailscale dial %s: %v; retry in %s\n", host, err, delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, 30*time.Second)
	}
}

func (n *tailnet) accept(ctx context.Context, sessions tcp.Sessions, listener net.Listener, allowed string) {
	closeListener := sync.OnceFunc(func() { listener.Close() })
	interrupt := context.AfterFunc(ctx, closeListener)
	defer interrupt()
	defer closeListener()
	var active net.Conn
	var session sync.WaitGroup
	closeActive := func() {
		if active != nil {
			active.Close()
			session.Wait()
		}
	}
	defer closeActive()
	for ctx.Err() == nil {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "link/tailscale accept:", err)
			}
			return
		}
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = n.admit(check, conn.RemoteAddr(), allowed)
		cancel()
		if err != nil {
			conn.Close()
			if ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "link/tailscale listen:", err)
			}
			continue
		}
		// This row pins one tailnet node. Its fresh, admitted TCP connection
		// can recover from an old half-open socket after that node restarts.
		closeActive()
		active = conn
		session.Go(func() {
			if err := n.session(ctx, sessions, conn, allowed); err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "link/tailscale session:", err)
			}
		})
	}
}

func (n *tailnet) session(ctx context.Context, sessions tcp.Sessions, conn net.Conn, peer string) error {
	done, err := tcp.Open(ctx, conn, sessions)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "link/tailscale connected %s path=%s\n", peer, n.path(ctx, peer))
	return <-done
}
