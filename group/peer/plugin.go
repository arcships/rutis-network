package peer

import (
	"context"
	"crypto/rand"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arcships/rutis-network/discovery/mdns"
	"github.com/arcships/rutis/go/rutis"
)

type discovery struct {
	Snapshot      func(context.Context) (mdns.Snapshot, error)
	WaitForChange func(context.Context, string) (mdns.Snapshot, error)
}

type group struct {
	row       context.Context
	source    discovery
	epoch     string
	mu        sync.Mutex
	revision  uint64
	observed  mdns.Snapshot
	expiry    time.Time
	timer     *time.Timer
	state     mdns.Snapshot
	sourceErr error
	changed   chan struct{}
	neighbors []Neighbor
	outbound  map[string]bool
}

func newGroup(row context.Context, source discovery, local string, neighbors []Neighbor) *group {
	epoch := rand.Text() + "/"
	g := &group{row: row, source: source, epoch: epoch, neighbors: neighbors, outbound: map[string]bool{},
		state: mdns.Snapshot{Cursor: epoch + "0", Candidates: []mdns.Candidate{}}, changed: make(chan struct{})}
	for _, n := range neighbors {
		if n.Dialer == local {
			g.outbound[n.Member] = true
		}
	}
	return g
}

func (g *group) Neighbors() []Neighbor { return slices.Clone(g.neighbors) }

type offerKey struct {
	id, kind string
	version  int
}

func (g *group) project(s mdns.Snapshot, now time.Time) ([]mdns.Candidate, time.Time) {
	result := []mdns.Candidate{}
	var expires time.Time
	count := map[offerKey]int{}
	for _, c := range s.Candidates {
		d := c.Description
		if d == nil || !g.outbound[d.ID] {
			continue
		}
		expiry, err := time.Parse(time.RFC3339Nano, c.ExpiresAt)
		if err != nil || !expiry.After(now) {
			continue
		}
		if expires.IsZero() || expiry.Before(expires) {
			expires = expiry
		}
		count[offerKey{d.ID, d.Kind, d.Version}]++
		result = append(result, c)
	}
	result = slices.DeleteFunc(result, func(c mdns.Candidate) bool {
		d := c.Description
		return count[offerKey{d.ID, d.Kind, d.Version}] > 1
	})
	return result, expires
}

func (g *group) update(observed mdns.Snapshot, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observed = observed
	g.publishLocked(err)
}

// Keep the full observation so expiry can also reveal a previously ambiguous offer.
func (g *group) publishLocked(err error) {
	next, expiry := g.project(g.observed, time.Now())
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	g.expiry = expiry
	if !expiry.IsZero() {
		g.timer = time.AfterFunc(time.Until(expiry), func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			// A stopped timer's callback may already be waiting for the lock.
			// Always use the current observation and preserve a later source error.
			if !g.expiry.IsZero() {
				g.publishLocked(nil)
			}
		})
	}
	if g.sourceErr == nil && err == nil && reflect.DeepEqual(g.state.Candidates, next) {
		return
	}
	g.revision++
	g.state = mdns.Snapshot{Cursor: g.epoch + strconv.FormatUint(g.revision, 10), Candidates: next}
	g.sourceErr = err
	close(g.changed)
	g.changed = make(chan struct{})
}

func (g *group) expireLocked() {
	if !g.expiry.IsZero() && !time.Now().Before(g.expiry) {
		g.publishLocked(nil)
	}
}

// One subscriber follows the discovery service on behalf of all adapters.
func (g *group) run(ctx context.Context) error {
	for ctx.Err() == nil {
		state, err := g.source.Snapshot(ctx)
		for err == nil && ctx.Err() == nil {
			if state.Cursor == "" {
				err = errors.New("discovery returned an empty cursor")
				break
			}
			g.update(state, nil)
			cursor := state.Cursor
			state, err = g.source.WaitForChange(ctx, cursor)
			if err == nil && state.Cursor == cursor {
				err = errors.New("discovery returned an unchanged cursor")
			}
		}
		if ctx.Err() != nil {
			break
		}
		g.update(mdns.Snapshot{}, err)
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	g.update(mdns.Snapshot{}, ctx.Err())
	return ctx.Err()
}

func (g *group) Snapshot() (mdns.Snapshot, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.row.Err(); err != nil {
		return mdns.Snapshot{}, err
	}
	if g.sourceErr != nil {
		return mdns.Snapshot{}, g.sourceErr
	}
	g.expireLocked()
	s := g.state
	s.Candidates = slices.Clone(s.Candidates)
	for i := range s.Candidates {
		c := &s.Candidates[i]
		d := *c.Description
		d.Params = slices.Clone(d.Params)
		c.Description = &d
		c.Addresses = slices.Clone(c.Addresses)
	}
	return s, nil
}

func (g *group) WaitForChange(ctx context.Context, cursor string) (mdns.Snapshot, error) {
	g.mu.Lock()
	g.expireLocked()
	current, changed := g.state.Cursor, g.changed
	g.mu.Unlock()
	if cursor == current {
		select {
		case <-ctx.Done():
			return mdns.Snapshot{}, ctx.Err()
		case <-g.row.Done():
			return mdns.Snapshot{}, g.row.Err()
		case <-changed:
		}
	}
	return g.Snapshot()
}

func schema() map[string]any {
	text := map[string]any{"type": "string", "minLength": 1}
	id := map[string]any{"type": "string", "pattern": memberID.String()}
	pair := map[string]any{"type": "array", "items": id, "minItems": 2, "maxItems": 2}
	pairs := map[string]any{"type": "array", "items": pair}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"source", "service", "local", "members", "topology"},
		"properties": map[string]any{
			"source": text, "service": text, "local": id,
			"members":  map[string]any{"type": "array", "minItems": 2, "uniqueItems": true, "items": id},
			"topology": map[string]any{"type": "string", "enum": []string{"point-to-point", "chain", "ring", "star", "tree", "full-mesh", "partial-mesh"}},
			"parents":  map[string]any{"type": "object", "additionalProperties": id},
			"edges":    pairs, "blockedDials": pairs,
			"maxNeighbors": map[string]any{"type": "integer", "minimum": 1},
		},
	}
}

func validService(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "#\x00\r\n") && !strings.HasPrefix(s, "rutis/")
}

var Plugin = rutis.Define(rutis.Plugin[config]{
	Name: "peer-group", Schema: schema(),
	Apply: func(row *rutis.Ctx, cfg config) error {
		if !validService(cfg.Source) || !validService(cfg.Service) || cfg.Source == cfg.Service {
			return errors.New("source and service must be distinct local service names")
		}
		neighbors, err := cfg.neighbors()
		if err != nil {
			return err
		}
		var source discovery
		if err := row.Use(cfg.Source, &source); err != nil {
			return err
		}
		g := newGroup(row, source, cfg.Local, neighbors)
		row.Provide(cfg.Service, g)
		row.Go(g.run)
		return nil
	},
})
