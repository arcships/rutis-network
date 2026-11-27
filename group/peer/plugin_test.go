package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/arcships/rutis-network/discovery/mdns"
)

func TestProjectOnlyUniqueLiveOutboundDescriptions(t *testing.T) {
	now := time.Now()
	g := newGroup(t.Context(), discovery{}, "a", []Neighbor{{"b", "a"}, {"c", "c"}, {"d", ""}})
	makeCandidate := func(id string) mdns.Candidate {
		return mdns.Candidate{Description: &mdns.Description{ID: id, Kind: "custom", Version: 2, Params: json.RawMessage(`{"nested":[1,"a"]}`)}, Instance: id, Interface: "lan", ExpiresAt: now.Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	}
	b := makeCandidate("b")
	expired := b
	expired.ExpiresAt = now.UTC().Format(time.RFC3339Nano)
	s := mdns.Snapshot{Cursor: "1", Candidates: []mdns.Candidate{makeCandidate("a"), makeCandidate("outside"), makeCandidate("c"), makeCandidate("d"), {}, expired, b}}
	first, _ := g.project(s, now)
	if len(first) != 1 || first[0].Description.ID != "b" || first[0].Interface != "lan" || !bytes.Equal(first[0].Description.Params, b.Description.Params) {
		t.Fatal(first)
	}
	second := b
	second.Instance = "another-publisher"
	other := makeCandidate("b")
	other.Description.Kind = "custom-alt"
	s.Candidates = append(s.Candidates, second, other)
	got, _ := g.project(s, now)
	if len(got) != 1 || got[0].Description.Kind != "custom-alt" {
		t.Fatal(got)
	}
	s.Candidates = s.Candidates[:len(s.Candidates)-2]
	if got, _ := g.project(s, now); len(got) != 1 {
		t.Fatal("withdrawn duplicate still masks member")
	}
	if got, _ := g.project(s, now.Add(time.Minute)); len(got) != 0 {
		t.Fatal("expired", got)
	}
}

func TestCursorReloadAndCancellation(t *testing.T) {
	row, unload := context.WithCancel(t.Context())
	defer unload()
	g := newGroup(row, discovery{}, "a", []Neighbor{{"b", "a"}})
	old := newGroup(t.Context(), discovery{}, "a", nil)
	initial, err := old.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	current, err := g.WaitForChange(t.Context(), initial.Cursor)
	if err != nil || current.Cursor == initial.Cursor {
		t.Fatal(current, err)
	}
	done := make(chan error, 1)
	go func() { _, err := g.WaitForChange(t.Context(), current.Cursor); done <- err }()
	unload()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	plan := g.Neighbors()
	plan[0].Dialer = "b"
	if !slices.Equal(g.Neighbors(), []Neighbor{{"b", "a"}}) {
		t.Fatal("mutable group plan")
	}
}

func TestExpirySourceFailureAndRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		failure := errors.New("discovery unavailable")
		fail := make(chan struct{})
		c := mdns.Candidate{Description: &mdns.Description{ID: "b", Kind: "custom", Version: 1, Params: json.RawMessage(`{"x":1}`)}, Addresses: []string{"127.0.0.1"}}
		restarts := 0
		g := newGroup(ctx, discovery{
			Snapshot: func(context.Context) (mdns.Snapshot, error) {
				restarts++
				c.ExpiresAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
				return mdns.Snapshot{Cursor: "discovery/1", Candidates: []mdns.Candidate{c}}, nil
			},
			WaitForChange: func(ctx context.Context, cursor string) (mdns.Snapshot, error) {
				if cursor != "discovery/1" {
					t.Error(cursor)
				}
				select {
				case <-ctx.Done():
					return mdns.Snapshot{}, ctx.Err()
				case <-fail:
					return mdns.Snapshot{}, failure
				}
			},
		}, "a", []Neighbor{{"b", "a"}})
		go g.run(ctx)
		synctest.Wait()
		first, err := g.Snapshot()
		if err != nil || len(first.Candidates) != 1 {
			t.Fatal(first, err)
		}
		first.Candidates[0].Description.Params[0] = 0
		first.Candidates[0].Addresses[0] = "other"
		fresh, _ := g.Snapshot()
		if !bytes.Equal(fresh.Candidates[0].Description.Params, c.Description.Params) || fresh.Candidates[0].Addresses[0] != "127.0.0.1" || c.Description.Params[0] != '{' {
			t.Fatal("mutable candidate")
		}
		time.Sleep(time.Minute)
		expired, err := g.WaitForChange(ctx, first.Cursor)
		if err != nil || len(expired.Candidates) != 0 || expired.Cursor == first.Cursor {
			t.Fatal(expired, err)
		}
		fail <- struct{}{}
		synctest.Wait()
		if _, err = g.WaitForChange(ctx, expired.Cursor); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		recovered, err := g.Snapshot()
		if err != nil || len(recovered.Candidates) != 1 || restarts != 2 {
			t.Fatal(recovered, err, restarts)
		}
	})
}
