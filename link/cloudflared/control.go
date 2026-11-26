package cloudflared

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

type tcpState struct {
	Role      string `json:"role"`
	Desired   string `json:"desired"`
	Phase     string `json:"phase"`
	BoundPort int    `json:"boundPort"`
	LastError string `json:"lastError"`
	RetryAt   string `json:"retryAt"`
}

type tcpControl struct {
	Configure func(context.Context, map[string]any) error
	Start     func(context.Context) error
	Stop      func(context.Context) error
	Status    func(context.Context) (tcpState, error)
}

type snapshot struct {
	Cursor     string `json:"cursor"`
	Role       string `json:"role"`
	Desired    string `json:"desired"`
	Phase      string `json:"phase"`
	Profile    string `json:"profile,omitempty"`
	Process    string `json:"process"`
	BoundPort  int    `json:"boundPort,omitempty"`
	EdgeReady  *bool  `json:"edgeReady"`
	TCPPhase   string `json:"tcpPhase,omitempty"`
	TCPError   string `json:"tcpError,omitempty"`
	TCPRetryAt string `json:"tcpRetryAt,omitempty"`
	LastError  string `json:"lastError,omitempty"`
	RetryAt    string `json:"retryAt,omitempty"`
}

type control struct {
	row context.Context
	cfg config
	tcp *tcpControl

	commands sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	// Only the worker changes ownership. Commands access it after joining the worker.
	ownsTCP bool

	mu       sync.Mutex
	state    snapshot
	instance string
	revision uint64
	changed  chan struct{}
}

func newControl(row context.Context, cfg config, tcp *tcpControl) *control {
	c := &control{row: row, cfg: cfg, tcp: tcp, instance: rand.Text(), changed: make(chan struct{})}
	c.update(func(s *snapshot) {
		*s = snapshot{Role: cfg.Role, Desired: "stopped", Phase: "idle", Profile: cfg.Profile, Process: "stopped"}
	})
	return c
}

func (c *control) update(f func(*snapshot)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	before := c.state
	f(&c.state)
	c.state.Cursor = before.Cursor
	if before != c.state {
		c.revision++
		c.state.Cursor = c.instance + "/" + strconv.FormatUint(c.revision, 10)
		close(c.changed)
		c.changed = make(chan struct{})
	}
}

func (c *control) Status() snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *control) WaitForChange(ctx context.Context, cursor string) (snapshot, error) {
	if err := c.row.Err(); err != nil {
		return snapshot{}, err
	}
	c.mu.Lock()
	s, changed := c.state, c.changed
	c.mu.Unlock()
	if cursor != s.Cursor {
		return s, nil
	}
	select {
	case <-ctx.Done():
		return snapshot{}, ctx.Err()
	case <-c.row.Done():
		return snapshot{}, c.row.Err()
	case <-changed:
		return c.Status(), nil
	}
}

func (c *control) stopTCP(ctx context.Context) error {
	if !c.ownsTCP {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.tcp.Stop(ctx); err != nil {
		return errors.New("tcp-stop-failed")
	}
	c.ownsTCP = false
	return nil
}

func (c *control) join(ctx context.Context) error {
	if c.cancel != nil {
		c.cancel()
		<-c.done
		c.cancel = nil
	}
	return c.stopTCP(ctx)
}

func (c *control) launch(profile string) {
	ctx, cancel := context.WithCancel(c.row)
	c.cancel = cancel
	done := make(chan struct{})
	c.done = done
	c.update(func(s *snapshot) {
		*s = snapshot{Role: c.cfg.Role, Profile: profile, Phase: "starting", Desired: "running", Process: "stopped"}
	})
	go func() {
		defer close(done)
		defer cancel()
		err := c.run(ctx, profile)
		// On an independent failure, stop any TCP instance that we already started.
		if ctx.Err() == nil && err != nil {
			if stopErr := c.stopTCP(ctx); stopErr != nil {
				err = stopErr
			}
			c.update(func(s *snapshot) {
				s.Phase, s.Process, s.LastError = "failed", "stopped", err.Error()
				s.BoundPort, s.EdgeReady, s.RetryAt = 0, nil, ""
			})
			fmt.Fprintln(os.Stderr, "link/cloudflared", c.cfg.Role, err)
		}
	}()
}

func (c *control) Start() error {
	c.commands.Lock()
	defer c.commands.Unlock()
	if err := c.row.Err(); err != nil {
		return err
	}
	s := c.Status()
	if s.Desired == "running" && s.Phase != "failed" {
		return nil
	}
	if err := c.join(c.row); err != nil {
		return err
	}
	c.launch(s.Profile)
	return nil
}

func (c *control) stop(ctx context.Context) error {
	c.commands.Lock()
	defer c.commands.Unlock()
	err := c.join(ctx)
	c.update(func(s *snapshot) {
		*s = snapshot{Role: s.Role, Profile: s.Profile, Phase: "stopped", Desired: "stopped", Process: "stopped"}
		if err != nil {
			s.Phase, s.LastError = "failed", err.Error()
		}
	})
	return err
}

func (c *control) Stop() error { return c.stop(c.row) }

func (c *control) Configure(patch map[string]any) error {
	c.commands.Lock()
	defer c.commands.Unlock()
	if err := c.row.Err(); err != nil {
		return err
	}
	name, ok := patch["profile"].(string)
	target, exists := c.cfg.Profiles[name]
	if c.cfg.Role != "dial" || len(patch) != 1 || !ok || !exists {
		return errors.New("configure requires a declared dial profile")
	}
	s := c.Status()
	if s.Profile == name {
		return nil
	}
	if _, _, err := target.readCredentials(); err != nil {
		return err
	}
	if err := c.join(c.row); err != nil {
		return err
	}
	if s.Desired == "running" {
		c.launch(name)
	} else {
		c.update(func(s *snapshot) { s.Profile = name })
	}
	return nil
}
