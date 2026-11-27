package mdns

import (
	"context"
	"crypto/rand"
	"reflect"
	"slices"
	"strconv"
	"sync"
)

// Snapshot is a full replacement view. Its cursor is opaque and scoped to a row lifetime.
type Snapshot struct {
	Cursor     string      `json:"cursor"`
	Candidates []Candidate `json:"candidates"`
}

type observations struct {
	row      context.Context
	mu       sync.Mutex
	epoch    string
	revision uint64
	state    Snapshot
	changed  chan struct{}
	err      error
}

func newObservations(row context.Context) *observations {
	epoch := rand.Text()
	return &observations{row: row, epoch: epoch, changed: make(chan struct{}), state: Snapshot{Cursor: epoch + "/0", Candidates: []Candidate{}}}
}

func (o *observations) update(next []Candidate) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if reflect.DeepEqual(o.state.Candidates, next) {
		return
	}
	o.revision++
	o.state = Snapshot{Cursor: o.epoch + "/" + strconv.FormatUint(o.revision, 10), Candidates: next}
	close(o.changed)
	o.changed = make(chan struct{})
}

func (o *observations) fail(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.err = err
	close(o.changed)
}

func (o *observations) Snapshot() (Snapshot, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.row.Err(); err != nil {
		return Snapshot{}, err
	}
	if o.err != nil {
		return Snapshot{}, o.err
	}
	s := o.state
	s.Candidates = slices.Clone(s.Candidates)
	for i := range s.Candidates {
		if d := s.Candidates[i].Description; d != nil {
			copyD := *d
			copyD.Params = slices.Clone(d.Params)
			s.Candidates[i].Description = &copyD
		}
		s.Candidates[i].Addresses = slices.Clone(s.Candidates[i].Addresses)
	}
	return s, nil
}

func (o *observations) WaitForChange(ctx context.Context, cursor string) (Snapshot, error) {
	o.mu.Lock()
	current, changed := o.state.Cursor, o.changed
	o.mu.Unlock()
	if current == cursor {
		select {
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		case <-o.row.Done():
			return Snapshot{}, o.row.Err()
		case <-changed:
		}
	}
	return o.Snapshot()
}
