package webrtclink

import (
	"context"
	"crypto/rand"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"github.com/arcships/rutis/go/rutis"
)

type settings struct {
	PairID    string `json:"pairId"`
	Policy    string `json:"iceTransportPolicy"`
	Reconnect bool   `json:"reconnect"`
}
type config struct {
	PairID           string `json:"pairId"`
	Policy           string `json:"iceTransportPolicy"`
	SignalingService string `json:"signalingService"`
	ControlService   string `json:"controlService"`
	AutoStart        *bool  `json:"autoStart"`
	Reconnect        *bool  `json:"reconnect"`
}
type snapshot struct {
	settings
	Desired    string `json:"desired"`
	Phase      string `json:"phase"`
	Route      string `json:"route"`
	Cursor     string `json:"cursor"`
	LastError  string `json:"lastError,omitempty"`
	RetryCount int    `json:"retryCount"`
	RetryAt    string `json:"retryAt,omitempty"`
}

type control struct {
	row                   context.Context
	signal                signaling
	sessions              tcp.Sessions
	commands              sync.Mutex
	cancel                context.CancelFunc
	done                  chan struct{}
	lastPair, lastAttempt string // worker writes, commands read only after join
	mu                    sync.Mutex
	state                 snapshot
	instance              string
	revision              uint64
	changed               chan struct{}
}

func (c *control) update(f func(*snapshot)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.state
	f(&c.state)
	if prev != c.state {
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
	s, ch := c.state, c.changed
	c.mu.Unlock()
	if s.Cursor != cursor {
		return s, c.row.Err()
	}
	select {
	case <-ch:
		return c.Status()
	case <-ctx.Done():
		return snapshot{}, ctx.Err()
	case <-c.row.Done():
		return snapshot{}, c.row.Err()
	}
}
func (c *control) run(ctx context.Context, opts settings) {
	if c.lastPair != opts.PairID {
		c.lastPair = opts.PairID
		c.lastAttempt = ""
	}
	delay := 500 * time.Millisecond
	for ctx.Err() == nil {
		c.update(func(s *snapshot) { s.Phase = "pairing"; s.Route = "unknown"; s.RetryAt = "" })
		g, err := c.acquire(ctx, opts.PairID, c.lastAttempt)
		if err == nil {
			c.lastAttempt = g.AttemptID
			c.update(func(s *snapshot) { s.Phase = "negotiating" })
			started := time.Now()
			err = c.connect(ctx, opts, g)
			if time.Since(started) >= time.Minute {
				delay = 500 * time.Millisecond
			}
		}
		if ctx.Err() != nil {
			return
		}
		code := "connection-failed"
		for _, e := range []error{errClosed, errProtocol, errGrant, errDenied, errTurn, errSignal} {
			if errors.Is(err, e) {
				code = e.Error()
				break
			}
		}
		retry := false
		c.update(func(s *snapshot) {
			s.LastError = code
			s.Route = "unknown"
			s.Phase = "failed"
			retry = s.Reconnect && code != errProtocol.Error() && code != errDenied.Error()
			if retry {
				s.Phase = "retrying"
				s.RetryCount++
				s.RetryAt = time.Now().Add(delay).UTC().Format(time.RFC3339Nano)
			}
		})
		if !retry || sleep(ctx, delay) != nil {
			return
		}
		delay = min(2*delay, 30*time.Second)
	}
}
func (c *control) launch(opts settings) {
	c.update(func(s *snapshot) {
		s.settings = opts
		s.Phase = "pairing"
		s.Desired = "running"
		s.Route = "unknown"
		s.LastError = ""
		s.RetryCount = 0
		s.RetryAt = ""
	})
	ctx, cancel := context.WithCancel(c.row)
	done := make(chan struct{})
	c.cancel = cancel
	c.done = done
	go func() { defer close(done); defer cancel(); c.run(ctx, opts) }()
}
func (c *control) join() {
	if c.cancel != nil {
		c.cancel()
		<-c.done
		c.cancel = nil
		c.done = nil
	}
}
func (c *control) Start() error {
	c.commands.Lock()
	defer c.commands.Unlock()
	s, err := c.Status()
	if err != nil {
		return err
	}
	if s.Desired != "running" || s.Phase == "failed" {
		c.join()
		c.launch(s.settings)
	}
	return nil
}
func (c *control) Stop() error {
	c.commands.Lock()
	defer c.commands.Unlock()
	c.join()
	c.update(func(s *snapshot) {
		s.Desired = "stopped"
		s.Phase = "stopped"
		s.Route = "unknown"
		s.RetryAt = ""
		s.LastError = ""
		s.RetryCount = 0
	})
	return nil
}
func (s settings) valid() bool {
	return strings.TrimSpace(s.PairID) != "" && (s.Policy == "all" || s.Policy == "relay")
}
func (c *control) Configure(patch map[string]any) error {
	c.commands.Lock()
	defer c.commands.Unlock()
	s, err := c.Status()
	if err != nil {
		return err
	}
	next := s.settings
	for k, v := range patch {
		switch k {
		case "pairId":
			next.PairID, _ = v.(string)
		case "iceTransportPolicy":
			next.Policy, _ = v.(string)
		case "reconnect":
			b, ok := v.(bool)
			if !ok {
				return errors.New("invalid reconnect")
			}
			next.Reconnect = b
		default:
			return errors.New("unsupported setting")
		}
	}
	if !next.valid() || patch == nil {
		return errors.New("invalid WebRTC settings")
	}
	if next == s.settings {
		return nil
	}
	if s.Desired == "running" && (next.PairID != s.PairID || next.Policy != s.Policy) {
		c.join()
		c.launch(next)
		return nil
	}
	var phase, last string
	c.update(func(s *snapshot) {
		s.settings = next
		phase, last = s.Phase, s.LastError
	})
	if phase == "retrying" && !next.Reconnect {
		c.join()
		c.update(func(s *snapshot) { s.Phase = "failed"; s.Route = "unknown"; s.RetryAt = "" })
	}
	if phase == "failed" && next.Reconnect && last != errDenied.Error() && last != errProtocol.Error() {
		c.join()
		c.launch(next)
	}
	return nil
}
func validService(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && !strings.HasPrefix(s, "rutis/") && !strings.ContainsAny(s, "#\x00\r\n")
}
func apply(row *rutis.Ctx, cfg config) error {
	opts := settings{
		PairID: cfg.PairID, Policy: cfg.Policy,
		Reconnect: cfg.Reconnect == nil || *cfg.Reconnect,
	}
	if opts.Policy == "" {
		opts.Policy = "all"
	}
	validControl := cfg.ControlService == "" || (validService(cfg.ControlService) && cfg.ControlService != cfg.SignalingService)
	if !opts.valid() || !validService(cfg.SignalingService) || !validControl {
		return errors.New("invalid WebRTC row configuration")
	}
	c := &control{row: row, instance: rand.Text(), changed: make(chan struct{})}
	c.state = snapshot{settings: opts, Desired: "stopped", Phase: "stopped", Route: "unknown", Cursor: c.instance + "/0"}
	if err := row.Use("rutis/sessions", &c.sessions); err != nil {
		return err
	}
	if err := row.Use(cfg.SignalingService, &c.signal); err != nil {
		return err
	}
	row.Effect(func(context.Context) error { return c.Stop() })
	if cfg.AutoStart == nil || *cfg.AutoStart {
		if err := c.Start(); err != nil {
			return err
		}
	}
	if cfg.ControlService != "" {
		row.Provide(cfg.ControlService, c)
	}
	return nil
}

var Plugin = rutis.Define(rutis.Plugin[config]{
	Name:   "link-webrtc",
	Inject: []string{"rutis/sessions"},
	Apply:  apply,
	Schema: map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"pairId", "signalingService"},
		"properties": map[string]any{
			"pairId":             map[string]any{"type": "string", "minLength": 1},
			"signalingService":   map[string]any{"type": "string", "minLength": 1},
			"controlService":     map[string]any{"type": "string", "minLength": 1},
			"iceTransportPolicy": map[string]any{"enum": []string{"all", "relay"}},
			"autoStart":          map[string]any{"type": "boolean"},
			"reconnect":          map[string]any{"type": "boolean"},
		},
	},
})
