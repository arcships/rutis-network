package peer

import (
	"slices"
	"testing"
)

func TestBothEndsAgreeOnEveryEdge(t *testing.T) {
	roster := []string{"b", "a", "c", "d"}
	for _, tc := range []struct {
		cfg config
		// Directed examples use the dialer's ID first. A blank owner denotes
		// the one explicitly unreachable edge in the full mesh.
		edges []pair
	}{
		{config{Topology: "point-to-point", Members: roster[:2]}, []pair{{"b", "a"}}},
		{config{Topology: "chain", Members: roster}, []pair{{"b", "a"}, {"a", "c"}, {"c", "d"}}},
		{config{Topology: "ring", Members: roster}, []pair{{"b", "a"}, {"a", "c"}, {"c", "d"}, {"d", "b"}}},
		{config{Topology: "star", Members: roster}, []pair{{"a", "b"}, {"c", "b"}, {"d", "b"}}},
		{config{Topology: "tree", Members: roster, Parents: map[string]string{"a": "b", "c": "a", "d": "a"}}, []pair{{"a", "b"}, {"c", "a"}, {"d", "a"}}},
		{config{Topology: "full-mesh", Members: roster, BlockedDials: [][]string{{"a", "b"}, {"c", "d"}, {"d", "c"}}}, []pair{{"b", "a"}, {"a", "c"}, {"a", "d"}, {"b", "c"}, {"b", "d"}}},
		{config{Topology: "partial-mesh", Members: roster, MaxNeighbors: 2, Edges: [][]string{{"b", "a"}, {"b", "c"}, {"b", "d"}, {"c", "d"}, {"a", "d"}}, BlockedDials: [][]string{{"a", "b"}, {"b", "c"}}}, []pair{{"b", "a"}, {"c", "b"}, {"c", "d"}, {"a", "d"}}},
	} {
		t.Run(tc.cfg.Topology, func(t *testing.T) {
			for _, m := range tc.cfg.Members {
				cfg := tc.cfg
				cfg.Local = m
				got, err := cfg.neighbors()
				if err != nil {
					t.Fatal(err)
				}
				want := map[string]string{}
				for _, p := range tc.edges {
					switch m {
					case p.from:
						want[p.to] = p.from
					case p.to:
						want[p.from] = p.from
					}
				}
				if cfg.Topology == "full-mesh" && (m == "c" || m == "d") {
					other := "c"
					if m == "c" {
						other = "d"
					}
					want[other] = ""
				}
				if len(got) != len(want) {
					t.Fatalf("%s: got %+v, want %v", m, got, want)
				}
				for _, n := range got {
					if owner, ok := want[n.Member]; !ok || n.Dialer != owner {
						t.Fatalf("%s: got %+v, want %v", m, n, want)
					}
					delete(want, n.Member)
				}
			}
		})
	}
}

func TestPartialMeshSkipsUnreachableBeforeReservingSlots(t *testing.T) {
	cfg := config{Topology: "partial-mesh", Local: "a", Members: []string{"a", "b", "c"}, MaxNeighbors: 1,
		Edges: [][]string{{"a", "b"}, {"b", "c"}, {"a", "c"}}, BlockedDials: [][]string{{"a", "b"}, {"b", "a"}}}
	got, err := cfg.neighbors()
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	cfg.Local = "b"
	got, err = cfg.neighbors()
	if err != nil || !slices.Equal(got, []Neighbor{{"c", "b"}}) {
		t.Fatal(got, err)
	}
}

func TestRejectInvalidTopology(t *testing.T) {
	for _, tc := range []config{
		{Topology: "ring", Members: []string{"a", "b"}},
		{Topology: "point-to-point", Members: []string{"a", "b", "c"}},
		{Topology: "chain", Members: []string{"a", "a"}},
		{Topology: "chain", Members: []string{"a", ""}},
		{Topology: "star", Members: []string{"b", "c"}},
		{Topology: "ring", Members: []string{"a", "b", "c"}, BlockedDials: [][]string{{"a", "b"}}},
		{Topology: "tree", Parents: map[string]string{"b": "c", "c": "b"}},
		{Topology: "tree", Parents: map[string]string{"a": "b", "b": "c"}},
		{Topology: "tree", Parents: map[string]string{"b": "a", "c": "outside"}},
		{Topology: "tree", Parents: map[string]string{"b": "a"}},
		{Topology: "full-mesh", BlockedDials: [][]string{{"a", "b", "c"}}},
		{Topology: "full-mesh", BlockedDials: [][]string{{"a", "a"}}},
		{Topology: "partial-mesh", MaxNeighbors: 1, Edges: [][]string{{"a", "b"}, {"b", "a"}}},
		{Topology: "partial-mesh", Edges: [][]string{{"a", "b"}}},
		{Topology: "unknown"},
	} {
		tc.Local = "a"
		if tc.Members == nil {
			tc.Members = []string{"a", "b", "c"}
		}
		if _, err := tc.neighbors(); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
}
