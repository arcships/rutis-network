package mdns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"syscall"
	"time"
)

type catalogue struct {
	mu     sync.RWMutex
	bodies map[string][]byte
}

func (c *catalogue) set(path string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if body == nil {
		delete(c.bodies, path)
	} else {
		c.bodies[path] = body
	}
}
func (c *catalogue) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	c.mu.RLock()
	body := c.bodies[r.URL.EscapedPath()]
	c.mu.RUnlock()
	if body == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// Use one interface-bound socket per address family, sharing the SRV port.
func offerListeners(ctx context.Context, iface *net.Interface, pin netip.Addr) ([]net.Listener, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	var v4, v6 bool
	for _, a := range addrs {
		prefix, err := netip.ParsePrefix(a.String())
		if err != nil {
			continue
		}
		ip := prefix.Addr().Unmap()
		if ip.IsUnspecified() || ip.IsMulticast() || pin.IsValid() && pin != ip {
			continue
		}
		v4 = v4 || ip.Is4()
		v6 = v6 || ip.Is6()
	}
	var binds []netip.Addr
	if pin.IsValid() && (v4 || v6) {
		if pin.Is6() && pin.IsLinkLocalUnicast() {
			pin = pin.WithZone(iface.Name)
		}
		binds = append(binds, pin)
	} else {
		if v4 {
			binds = append(binds, netip.IPv4Unspecified())
		}
		if v6 {
			binds = append(binds, netip.IPv6Unspecified())
		}
	}
	if len(binds) == 0 {
		return nil, fmt.Errorf("no matching unicast address on %s", iface.Name)
	}
	lc := net.ListenConfig{Control: interfaceControl(iface)}
	// Port zero is allocated per family. Retry if a socket in the other family
	// already uses the ephemeral port chosen by the first bind.
	for attempt := 0; attempt < 8; attempt++ {
		var port uint16
		var listeners []net.Listener
		for _, ip := range binds {
			network := "tcp4"
			if ip.Is6() {
				network = "tcp6"
			}
			var l net.Listener
			l, err = lc.Listen(ctx, network, netip.AddrPortFrom(ip, port).String())
			if err != nil {
				break
			}
			port = l.Addr().(*net.TCPAddr).AddrPort().Port()
			listeners = append(listeners, l)
		}
		if err == nil {
			return listeners, nil
		}
		for _, l := range listeners {
			l.Close()
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			break
		}
	}
	return nil, err
}

func (c *catalogue) serve(ctx context.Context, listeners []net.Listener, updates <-chan []Description, index int, pin netip.Addr) error {
	server := &http.Server{Handler: c, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 4096}
	serverErr := make(chan error, len(listeners))
	var workers sync.WaitGroup
	for _, l := range listeners {
		workers.Go(func() { serverErr <- server.Serve(l) })
	}
	defer func() { server.Close(); workers.Wait() }()
	return c.publish(ctx, updates, serverErr, index, listeners[0].Addr().(*net.TCPAddr).Port, pin)
}
