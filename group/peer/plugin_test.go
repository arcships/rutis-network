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

func TestExpiryAdvancesCursorWhileDiscoveryIsSilent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		now := time.Now()
		candidate := func(id, instance string, ttl time.Duration) mdns.Candidate {
			return mdns.Candidate{
				Description: &mdns.Description{ID: id, Kind: "custom", Version: 1},
				Instance:    instance, ExpiresAt: now.Add(ttl).Format(time.RFC3339Nano),
			}
		}
		source := mdns.Snapshot{Cursor: "lan/1", Candidates: []mdns.Candidate{
			candidate("b", "b", time.Minute),
			candidate("c", "c1", time.Minute),
			candidate("c", "c2", 2*time.Minute),
		}}
		g := newGroup(ctx, discovery{
			Snapshot: func(context.Context) (mdns.Snapshot, error) { return source, nil },
			WaitForChange: func(context.Context, string) (mdns.Snapshot, error) {
				<-ctx.Done() // The upstream sends no updates during either lease transition.
				return mdns.Snapshot{}, ctx.Err()
			},
		}, "a", []Neighbor{{"b", "a"}, {"c", "a"}})
		go g.run(ctx)
		synctest.Wait()
		first, err := g.Snapshot()
		if err != nil || len(first.Candidates) != 1 || first.Candidates[0].Instance != "b" {
			t.Fatal(first, err)
		}
		time.Sleep(time.Minute)
		second, err := g.Snapshot()
		if err != nil || second.Cursor == first.Cursor || len(second.Candidates) != 1 || second.Candidates[0].Instance != "c2" {
			t.Fatalf("expiry must remove b and reveal c2 at a new cursor: %+v, %v", second, err)
		}
		observed, err := g.WaitForChange(ctx, first.Cursor)
		if err != nil || observed.Cursor != second.Cursor || len(observed.Candidates) != 1 || observed.Candidates[0].Instance != "c2" {
			t.Fatal(observed, err)
		}
		// With no other reader, a subscriber must also wake on c2's expiry.
		wait, stop := context.WithTimeout(ctx, 2*time.Minute)
		defer stop()
		last, err := g.WaitForChange(wait, second.Cursor)
		if err != nil || last.Cursor == second.Cursor || len(last.Candidates) != 0 || !time.Now().Equal(now.Add(2*time.Minute)) {
			t.Fatal(last, err, time.Now())
		}
	})
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

func TestOldDeadlineAfterRenewalAndSourceLoss(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newGroup(t.Context(), discovery{}, "a", []Neighbor{{"b", "a"}})
		now := time.Now()
		announce := func(ttl time.Duration) {
			g.update(mdns.Snapshot{Candidates: []mdns.Candidate{{
				Description: &mdns.Description{ID: "b", Kind: "custom", Version: 1},
				ExpiresAt:   now.Add(ttl).Format(time.RFC3339Nano),
			}}}, nil)
		}
		announce(time.Minute)
		announce(2 * time.Minute)
		renewed, err := g.Snapshot()
		if err != nil || len(renewed.Candidates) != 1 {
			t.Fatal(renewed, err)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		retained, err := g.Snapshot()
		if err != nil || retained.Cursor != renewed.Cursor || len(retained.Candidates) != 1 {
			t.Fatal(retained, err)
		}
		lost := errors.New("discovery unavailable")
		g.update(mdns.Snapshot{}, lost)
		time.Sleep(time.Minute)
		synctest.Wait()
		if _, err := g.Snapshot(); !errors.Is(err, lost) {
			t.Fatalf("old lease cleared source failure: %v", err)
		}
	})
}
