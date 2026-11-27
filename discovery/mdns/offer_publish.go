package mdns

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/arcships/rutis/go/rutis"
)

type offerSource struct {
	Snapshot      func(context.Context) (any, error)
	WaitForChange func(context.Context, string) (any, error)
}

const maxOffers = 128

// Decode items separately so a mistyped field in one plugin's offer cannot
// turn the entire source reply into a failed RPC. A malformed duplicate still
// names its ID, which must be suppressed along with the other duplicate.
func decodeSnapshot(raw any) (SourceSnapshot, error) {
	if state, ok := raw.(SourceSnapshot); ok {
		if state.Cursor == "" || len(state.Offers) > maxOffers {
			return SourceSnapshot{}, errors.New("invalid source cursor or offer count")
		}
		return state, nil
	}
	record, _ := raw.(map[string]any)
	cursor, _ := record["cursor"].(string)
	items, ok := record["offers"].([]any)
	if cursor == "" || !ok {
		return SourceSnapshot{}, errors.New("source requires a cursor and an offers array")
	}
	if len(items) > maxOffers {
		return SourceSnapshot{}, fmt.Errorf("source exceeds %d offers", maxOffers)
	}
	state := SourceSnapshot{Cursor: cursor, Offers: make([]Description, 0, len(items))}
	for _, item := range items {
		var d Description
		data, err := json.Marshal(item)
		if err != nil || json.Unmarshal(data, &d) != nil {
			fields, _ := item.(map[string]any)
			id, _ := fields["id"].(string)
			d = Description{ID: id}
		}
		state.Offers = append(state.Offers, d)
	}
	return state, nil
}

func readSource(ctx context.Context, s offerSource, updates chan<- []Description) {
	for ctx.Err() == nil {
		raw, callErr := s.Snapshot(ctx)
		var cursor string
		for callErr == nil && ctx.Err() == nil {
			state, err := decodeSnapshot(raw)
			if err == nil && state.Cursor == cursor {
				err = errors.New("source returned unchanged cursor")
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "mDNS rejected source snapshot:", err)
				break // Keep the last accepted snapshot and re-read the source.
			}
			select {
			case updates <- state.Offers:
			case <-ctx.Done():
				return
			}
			cursor = state.Cursor
			raw, callErr = s.WaitForChange(ctx, cursor)
		}
		if ctx.Err() != nil {
			return
		}
		if callErr != nil {
			fmt.Fprintln(os.Stderr, "mDNS offer source:", callErr)
			select {
			case updates <- nil:
			case <-ctx.Done():
				return
			}
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
	// A native source can pass its JSON bytes directly. Other runtimes
	// provide rutis records, whose individual offers are decoded independently.
	var native interface {
		Snapshot(context.Context) (SourceSnapshot, error)
		WaitForChange(context.Context, string) (SourceSnapshot, error)
	}
	if err := row.Use(cfg.Source, &native); err == nil {
		source.Snapshot = func(ctx context.Context) (any, error) { return native.Snapshot(ctx) }
		source.WaitForChange = func(ctx context.Context, cursor string) (any, error) { return native.WaitForChange(ctx, cursor) }
	} else if err := row.Use(cfg.Source, &source); err != nil {
		return err
	}
	listeners, err := offerListeners(row, iface, cfg.listenAddr)
	if err != nil {
		return err
	}
	c := &catalogue{bodies: map[string][]byte{}}
	row.Go(func(ctx context.Context) error {
		ctx, cancel := context.WithCancel(ctx)
		updates := make(chan []Description)
		done := make(chan struct{})
		go func() { defer close(done); readSource(ctx, source, updates) }()
		defer func() { cancel(); <-done }()
		return c.serve(ctx, listeners, updates, iface.Index, cfg.listenAddr)
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

func snapshotOffers(items []Description, sequence uint64, pin netip.Addr) map[string]encodedOffer {
	desired := map[string]encodedOffer{}
	seen := map[string]bool{}
	for _, d := range items {
		if seen[d.ID] {
			delete(desired, d.ID)
			fmt.Fprintf(os.Stderr, "duplicate mDNS offer ID %q\n", d.ID)
			continue
		}
		seen[d.ID] = true
		e, err := encodeOffer(d, sequence, pin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid mDNS offer:", err)
			continue
		}
		desired[d.ID] = e
	}
	return desired
}

func (c *catalogue) publish(ctx context.Context, updates <-chan []Description, serverErr <-chan error, index, port int, pin netip.Addr) error {
	var sequence uint64
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
		sequence++
		desired := snapshotOffers(items, sequence, pin)
		for id, a := range active {
			if _, ok := desired[id]; !ok {
				stop(id, a)
			}
		}
		for id, e := range desired {
			a := active[id]
			if a != nil && a.encoded.rev == e.rev && a.encoded.kind == e.kind && a.encoded.version == e.version {
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
