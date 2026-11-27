package linkssh

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"github.com/arcships/rutis/go/rutis"
)

type snapshot struct {
	Cursor          string   `json:"cursor"`
	Desired         string   `json:"desired"`
	Phase           string   `json:"phase"`
	Config          settings `json:"config"`
	LocalAddress    string   `json:"localAddress,omitempty"`
	RemoteAddress   string   `json:"remoteAddress,omitempty"`
	BoundAddress    string   `json:"boundAddress,omitempty"`
	Connections     int      `json:"connections"`
	LastClientError string   `json:"lastClientError,omitempty"`
	LastError       string   `json:"lastError,omitempty"`
	RetryCount      int      `json:"retryCount"`
	RetryAt         string   `json:"retryAt,omitempty"`
}

type control struct {
	role     role
	row      context.Context
	profiles map[string]profile
	sessions tcp.Sessions

	commands sync.Mutex // serialize commands until the previous worker releases its session
	cancel   context.CancelFunc
	done     chan struct{}

	mu       sync.Mutex
	state    snapshot
	instance string
	revision uint64
	changed  chan struct{}
}

func (c *control) update(f func(*snapshot)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	before := c.state
	f(&c.state)
	c.state.Cursor = before.Cursor
	if c.state != before {
		c.revision++
		c.state.Cursor = c.instance + "/" + strconv.FormatUint(c.revision, 10)
		close(c.changed)
		c.changed = make(chan struct{})
	}
}

func (c *control) Status() (snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state, c.row.Err()
}

func (c *control) WaitForChange(ctx context.Context, cursor string) (snapshot, error) {
	c.mu.Lock()
	s, changed := c.state, c.changed
	c.mu.Unlock()
	if s.Cursor != cursor {
		return s, c.row.Err()
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

func (c *control) run(ctx context.Context, e endpoint) {
	const initial = 500 * time.Millisecond
	backoff := initial
	for ctx.Err() == nil {
		done, err := c.open(ctx, e)
		if err == nil {
			connected := time.Now()
			err = <-done
			if time.Since(connected) >= time.Minute {
				backoff = initial
			}
		}
		if ctx.Err() != nil {
			return
		}
		code := errorCode(err)
		delay := retryDelay(code, backoff)
		c.update(func(s *snapshot) {
			s.LastError, s.LocalAddress, s.RemoteAddress, s.BoundAddress = code, "", "", ""
			if !s.Config.Reconnect {
				delay = 0
			}
			if delay == 0 {
				s.Phase, s.RetryAt = "failed", ""
			} else {
				s.Phase = "retrying"
				s.RetryAt = time.Now().Add(delay).UTC().Format(time.RFC3339Nano)
				s.RetryCount++
			}
		})
		fmt.Fprintln(os.Stderr, "link/ssh", e.SSHAddress, e.relayAddress(), code)
		if delay == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		backoff = min(30*time.Second, 2*backoff)
	}
}

func (c *control) join() {
	if c.cancel != nil {
		c.cancel()
		<-c.done
		c.cancel, c.done = nil, nil
	}
}

func (c *control) launch(cfg settings) {
	c.update(func(s *snapshot) {
		*s = snapshot{Desired: "running", Phase: "awaiting-config", Config: cfg}
		if cfg.ready(c.role) {
			s.Phase = "dialing"
		}
	})
	if !cfg.ready(c.role) {
		return
	}
	ctx, cancel := context.WithCancel(c.row)
	done := make(chan struct{})
	c.cancel, c.done = cancel, done
	go func() {
		defer close(done)
		defer cancel()
		c.run(ctx, cfg.endpoint)
	}()
}

func (c *control) Start() error {
	c.commands.Lock()
	defer c.commands.Unlock()
	s, err := c.Status()
	if err != nil {
		return err
	}
	if !s.Config.ready(c.role) {
		return errors.New("configure the SSH endpoint and credential profile before starting")
	}
	if s.Desired != "running" || s.Phase == "failed" {
		c.join()
		c.launch(s.Config)
	}
	return nil
}

func (c *control) Stop() error {
	c.commands.Lock()
	defer c.commands.Unlock()
	c.join()
	c.update(func(s *snapshot) { *s = snapshot{Desired: "stopped", Phase: "stopped", Config: s.Config} })
	return c.row.Err()
}

func (c *control) Configure(patch map[string]any) error {
	c.commands.Lock()
	defer c.commands.Unlock()
	s, err := c.Status()
	if err != nil {
		return err
	}
	next, err := s.Config.patch(patch, c.role, c.profiles)
	if err != nil || next == s.Config {
		return err
	}
	if next.endpoint != s.Config.endpoint && s.Desired == "running" {
		c.join()
		c.launch(next)
		return nil
	}
	// Decide the new retry policy under the same lock as the worker's failure.
	var phase, last string
	c.update(func(s *snapshot) {
		s.Config = next
		phase, last = s.Phase, s.LastError
	})
	if s.Desired == "running" {
		if phase == "retrying" && !next.Reconnect {
			c.join()
			c.update(func(s *snapshot) {
				s.Phase, s.RetryAt, s.LocalAddress, s.RemoteAddress, s.BoundAddress = "failed", "", "", "", ""
			})
		} else if phase == "failed" && next.Reconnect && retryDelay(last, time.Second) > 0 {
			c.join()
			c.launch(next)
		}
	}
	return nil
}

var (
	Dial   = define("link-ssh-dial", dialRole)
	Listen = define("link-ssh-listen", listenRole)
	Tunnel = define("link-ssh-tunnel", tunnelRole)
)

func define(name string, r role) *rutis.Definition {
	var inject []string
	if r != tunnelRole {
		inject = []string{"rutis/sessions"}
	}
	return rutis.Define(rutis.Plugin[config]{
		Name: name, Inject: inject, Schema: schema(r),
		Apply: func(row *rutis.Ctx, cfg config) error { return apply(row, cfg, r) },
	})
}

func apply(row *rutis.Ctx, cfg config, r role) error {
	if err := cfg.validate(r); err != nil {
		return err
	}
	c := &control{role: r, row: row, profiles: cfg.Profiles, instance: rand.Text(), changed: make(chan struct{})}
	if r != tunnelRole {
		if err := row.Use("rutis/sessions", &c.sessions); err != nil {
			return err
		}
	}
	options := settings{endpoint: cfg.endpoint, Reconnect: cfg.ControlService != "" || r != dialRole}
	if cfg.Reconnect != nil {
		options.Reconnect = *cfg.Reconnect
	}
	if cfg.ControlService == "" && !options.Reconnect {
		done, err := c.open(row, cfg.endpoint)
		if err != nil {
			return fmt.Errorf("link/ssh %s %s %s", cfg.SSHAddress, cfg.relayAddress(), errorCode(err))
		}
		row.Go(func(ctx context.Context) error {
			if err := <-done; err != nil && ctx.Err() == nil {
				return errors.New(errorCode(err))
			}
			return nil
		})
		return nil
	}
	row.Effect(func(context.Context) error { c.Stop(); return nil })
	if cfg.AutoStart == nil || *cfg.AutoStart {
		c.launch(options)
	} else {
		c.update(func(s *snapshot) { *s = snapshot{Desired: "stopped", Phase: "idle", Config: options} })
	}
	if cfg.ControlService != "" {
		row.Provide(cfg.ControlService, c)
	}
	return nil
}
