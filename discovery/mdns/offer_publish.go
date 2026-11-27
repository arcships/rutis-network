package mdns

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/arcships/rutis/go/rutis"
)

type offerSource struct {
	Snapshot      func(context.Context) (SourceSnapshot, error)
	WaitForChange func(context.Context, string) (SourceSnapshot, error)
}

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

func offerListener(ctx context.Context, iface *net.Interface, listenIP string) (net.Listener, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	var chosen netip.Addr
	for _, a := range addrs {
		p, err := netip.ParsePrefix(a.String())
		if err != nil {
			continue
		}
		ip := p.Addr().Unmap()
		if ip.IsUnspecified() || ip.IsMulticast() {
			continue
		}
		if listenIP != "" && listenIP != ip.String() {
			continue
		}
		if !chosen.IsValid() || ip.Is4() {
			chosen = ip
		}
	}
	if !chosen.IsValid() {
		return nil, fmt.Errorf("no matching unicast listenIP on %s", iface.Name)
	}
	network := "tcp4"
	if chosen.Is6() {
		network = "tcp6"
		if chosen.IsLinkLocalUnicast() {
			chosen = chosen.WithZone(iface.Name)
		}
	}
	lc := net.ListenConfig{Control: interfaceControl(iface)}
	return lc.Listen(ctx, network, netip.AddrPortFrom(chosen, 0).String())
}

func readSource(ctx context.Context, s offerSource, updates chan<- []Description) {
	for ctx.Err() == nil {
		state, err := s.Snapshot(ctx)
		for err == nil && ctx.Err() == nil {
			if state.Cursor == "" {
				err = errors.New("source returned empty cursor")
				break
			}
			select {
			case updates <- state.Offers:
			case <-ctx.Done():
				return
			}
			prev := state.Cursor
			state, err = s.WaitForChange(ctx, prev)
			if err == nil && prev == state.Cursor {
				err = errors.New("source returned unchanged cursor")
			}
		}
		if ctx.Err() != nil {
			return
		}
		fmt.Fprintln(os.Stderr, "mDNS offer source:", err)
		select {
		case updates <- nil:
		case <-ctx.Done():
			return
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

type activeOffer struct {
	encoded encodedOffer
	updates chan encodedOffer
	cancel  context.CancelFunc
	done    chan struct{}
}

func publishSource(row *rutis.Ctx, cfg *offerConfig, iface *net.Interface) error {
	var source offerSource
	if err := row.Use(cfg.Source, &source); err != nil {
		return err
	}
	l, err := offerListener(row, iface, cfg.ListenIP)
	if err != nil {
		return err
	}
	c := &catalogue{bodies: map[string][]byte{}}
	server := &http.Server{Handler: c, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 4096}
	row.Go(func(ctx context.Context) error {
		ctx, cancel := context.WithCancel(ctx)
		var workers sync.WaitGroup
		updates := make(chan []Description)
		serverErr := make(chan error, 1)
		workers.Go(func() { serverErr <- server.Serve(l) })
		workers.Go(func() { readSource(ctx, source, updates) })
		defer func() { cancel(); server.Close(); workers.Wait() }()
		return c.publish(ctx, updates, serverErr, iface.Index, l.Addr().(*net.TCPAddr).Port)
	})
	return nil
}

func updateRegistration(ctx context.Context, reg *publication, current *encodedOffer, updates <-chan encodedOffer) error {
	run, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { defer close(done); done <- reg.run(run) }()
	defer func() { cancel(); <-done }()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			return err
		case *current = <-updates:
			if err := reg.update(current.txt); err != nil {
				return err
			}
		}
	}
}

// Each instance owns its registration so a slow daemon acknowledgement cannot
// delay another offer's withdrawal. A buffered snapshot replaces pending edits.
func maintainOffer(ctx context.Context, first encodedOffer, updates <-chan encodedOffer, name string, index, port int) {
	current := first
	for ctx.Err() == nil {
		reg, err := startPublisher(ctx, advert{Type: offerType, Instance: name, Port: port, txt: current.txt}, index)
		if err == nil {
			err = updateRegistration(ctx, reg, &current, updates)
		}
		if ctx.Err() != nil {
			return
		}
		fmt.Fprintln(os.Stderr, "mDNS registration:", err)
		select {
		case <-ctx.Done():
			return
		case current = <-updates:
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *catalogue) publish(ctx context.Context, updates <-chan []Description, serverErr <-chan error, index, port int) error {
	active := map[string]*activeOffer{}
	suffix := rand.Text()[:10]
	stop := func(id string, a *activeOffer) {
		c.set(a.encoded.path(), nil)
		a.cancel()
		<-a.done
		delete(active, id)
	}
	defer func() {
		for _, a := range active {
			a.cancel()
		}
		for id, a := range active {
			stop(id, a)
		}
	}()
	for {
		var items []Description
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-serverErr:
			return err
		case items = <-updates:
		}
		desired := map[string]encodedOffer{}
		if len(items) > 128 {
			fmt.Fprintln(os.Stderr, "mDNS source exceeds 128 offers")
		} else {
			for _, d := range items {
				e, err := encodeOffer(d)
				if _, dup := desired[d.ID]; err != nil || dup {
					fmt.Fprintln(os.Stderr, "invalid or duplicate offer in mDNS source snapshot")
					clear(desired)
					break
				}
				desired[d.ID] = e
			}
		}
		for id, a := range active {
			if _, ok := desired[id]; !ok {
				stop(id, a)
			}
		}
		for id, e := range desired {
			a := active[id]
			if a != nil && bytes.Equal(a.encoded.txt, e.txt) {
				continue
			}
			c.set(e.path(), e.body)
			if a != nil {
				if a.encoded.path() != e.path() {
					c.set(a.encoded.path(), nil)
				}
				select {
				case <-a.updates:
				default:
				}
				a.updates <- e
				a.encoded = e
				continue
			}
			// DNS labels are case insensitive, public IDs are case sensitive.
			name := strings.ToLower(id[:min(len(id), 40)]) + "-" + digest([]byte(id))[:8] + "-" + suffix
			work, cancel := context.WithCancel(ctx)
			a = &activeOffer{e, make(chan encodedOffer, 1), cancel, make(chan struct{})}
			active[id] = a
			go func() { defer close(a.done); maintainOffer(work, e, a.updates, name, index, port) }()
		}
	}
}
