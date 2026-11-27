package tcp

import (
	"context"
	"crypto/rand"
	"errors"
	"strconv"
	"strings"
	"sync"
)

// Only the exported methods of control are published by the Go SDK.
type control struct {
	row      context.Context
	config   config
	sessions Sessions
	dial     bool

	// Serialize lifecycle changes through cancellation and worker exit.
	// Network callbacks take only mu, so a command can wait for them here.
	commands sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}

	mu       sync.Mutex
	state    snapshot
	instance string
	revision uint64
	changed  chan struct{}
}

type snapshot struct {
	Cursor        string   `json:"cursor"`
	Role          string   `json:"role"`
	Desired       string   `json:"desired"`
	Phase         string   `json:"phase"`
	Config        settings `json:"config"`
	LocalAddress  string   `json:"localAddress,omitempty"`
	RemoteAddress string   `json:"remoteAddress,omitempty"`
	BoundPort     int      `json:"boundPort,omitempty"`
	LastError     string   `json:"lastError,omitempty"`
	RetryCount    int      `json:"retryCount"`
	RetryAt       string   `json:"retryAt,omitempty"`
	Connections   int      `json:"connections"`
}

func validServiceName(name string) bool {
	return strings.TrimSpace(name) == name && !strings.ContainsAny(name, "#\x00\r\n") && !strings.HasPrefix(name, "rutis/")
}

func newControl(row context.Context, cfg config, sessions Sessions, dial bool, options settings) *control {
	role := "listen"
	if dial {
		role = "dial"
	}
	c := &control{
		row: row, config: cfg, sessions: sessions, dial: dial,
		instance: rand.Text(), changed: make(chan struct{}),
		state: snapshot{Role: role, Desired: "stopped", Phase: "idle", Config: options},
	}
	c.notify()
	return c
}

// notify publishes a new cursor while mu is held (or before publication).
func (c *control) notify() {
	c.revision++
	c.state.Cursor = c.instance + "/" + strconv.FormatUint(c.revision, 10)
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *control) update(f func(*snapshot)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.state
	f(&c.state)
	if old != c.state {
		c.notify()
	}
}

func (c *control) phase(name string) {
	c.update(func(s *snapshot) {
		s.Phase = name
		s.RetryAt = ""
	})
}

func (s snapshot) clone() snapshot {
	if s.Config.Port != nil {
		port := *s.Config.Port
		s.Config.Port = &port
	}
	return s
}

func (c *control) snapshot() snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.clone()
}

func (c *control) Status() (snapshot, error) {
	if err := c.row.Err(); err != nil {
		return snapshot{}, err
	}
	return c.snapshot(), nil
}

func (c *control) WaitForChange(ctx context.Context, cursor string) (snapshot, error) {
	if err := c.row.Err(); err != nil {
		return snapshot{}, err
	}
	c.mu.Lock()
	current, changed := c.state, c.changed
	c.mu.Unlock()
	if cursor != current.Cursor {
		return current.clone(), nil
	}
	select {
	case <-ctx.Done():
		return snapshot{}, ctx.Err()
	case <-c.row.Done():
		return snapshot{}, c.row.Err()
	case <-changed:
		return c.Status()
	}
}

func (c *control) join() {
	if c.cancel != nil {
		c.cancel()
		<-c.done
		c.cancel = nil
		c.done = nil
	}
}

func (c *control) launch(options settings) {
	ctx, cancel := context.WithCancel(c.row)
	done := make(chan struct{})
	c.cancel, c.done = cancel, done
	phase := "starting"
	if c.dial {
		phase = "dialing"
	}
	c.update(func(s *snapshot) {
		*s = snapshot{Role: s.Role, Desired: "running", Phase: phase, Config: options}
	})
	go func() {
		defer close(done)
		defer cancel()
		if c.dial {
			c.dialLoop(ctx, options.target)
			return
		}
		listener, identity, err := c.bind(ctx, options.target)
		if err != nil {
			c.update(func(s *snapshot) {
				s.Phase = "failed"
				s.LastError = TransportErrorCode(err)
			})
			return
		}
		c.accept(ctx, listener, identity, maxPendingHandshakes)
	}()
}

func (c *control) setRunning() {
	s := c.snapshot()
	if s.Config.Port == nil {
		c.update(func(s *snapshot) {
			s.Desired = "running"
			s.Phase = "awaiting-config"
		})
	} else if c.cancel == nil || s.Phase == "failed" {
		c.join()
		c.launch(s.Config)
	}
}

func (c *control) Start() error {
	c.commands.Lock()
	defer c.commands.Unlock()
	if err := c.row.Err(); err != nil {
		return err
	}
	if c.snapshot().Config.Port == nil {
		return errors.New("configure port before starting")
	}
	c.setRunning()
	return nil
}

func (c *control) Stop() error {
	if err := c.row.Err(); err != nil {
		return err
	}
	return c.stop()
}

func (c *control) stop() error {
	c.commands.Lock()
	defer c.commands.Unlock()
	c.update(func(s *snapshot) { s.Desired = "stopped" })
	c.join()
	c.update(func(s *snapshot) {
		s.Phase = "stopped"
		s.LocalAddress = ""
		s.RemoteAddress = ""
		s.BoundPort = 0
		s.RetryAt = ""
		s.Connections = 0
	})
	return nil
}

func (c *control) Configure(patch map[string]any) error {
	c.commands.Lock()
	defer c.commands.Unlock()
	if err := c.row.Err(); err != nil {
		return err
	}
	old := c.snapshot()
	next, err := old.Config.patch(patch, c.dial)
	if err != nil {
		return err
	}
	if !old.Config.target.same(next.target) {
		c.join()
		if old.Desired == "running" && next.Port != nil {
			c.launch(next)
		} else {
			c.update(func(s *snapshot) {
				*s = snapshot{Role: s.Role, Desired: s.Desired, Phase: s.Phase, Config: next}
			})
		}
		return nil
	}
	if next.Reconnect == old.Config.Reconnect {
		return nil
	}

	// Observe the phase and change policy under the same lock the worker uses
	// to decide whether a disconnect schedules another attempt.
	c.mu.Lock()
	phase, last := c.state.Phase, c.state.LastError
	c.state.Config.Reconnect = next.Reconnect
	c.notify()
	c.mu.Unlock()
	if !next.Reconnect && phase == "retrying" {
		c.join()
		c.update(func(s *snapshot) {
			s.Phase = "failed"
			s.LocalAddress = ""
			s.RemoteAddress = ""
			s.RetryAt = ""
		})
	} else if next.Reconnect && old.Desired == "running" && phase == "failed" && last != "incompatible-tls" {
		c.join()
		c.launch(next)
	}
	return nil
}
