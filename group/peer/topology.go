// Package peer selects the local neighbors and dial ownership of a configured group.
package peer

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type config struct {
	Source       string            `json:"source"`
	Service      string            `json:"service"`
	Local        string            `json:"local"`
	Members      []string          `json:"members"`
	Topology     string            `json:"topology"`
	Parents      map[string]string `json:"parents,omitempty"`
	Edges        [][]string        `json:"edges,omitempty"`
	MaxNeighbors int               `json:"maxNeighbors,omitempty"`
	BlockedDials [][]string        `json:"blockedDials,omitempty"`
}

var memberID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,63}$`)

// Neighbor is a desired adjacency. Dialer is empty if neither direction is reachable.
type Neighbor struct {
	Member string `json:"member"`
	Dialer string `json:"dialer"`
}

type pair struct{ from, to string }

func checkedPair(raw []string, members map[string]bool) (pair, error) {
	if len(raw) != 2 || !members[raw[0]] || !members[raw[1]] || raw[0] == raw[1] {
		return pair{}, fmt.Errorf("expected two distinct roster members, got %q", raw)
	}
	return pair{raw[0], raw[1]}, nil
}

// Both hosts use the same directed reachability policy. The presence of a
// local advertisement cannot establish whether the reverse route is usable.
func meshDialer(a, b string, blocked map[pair]bool) string {
	switch {
	case blocked[pair{a, b}] && blocked[pair{b, a}]:
		return ""
	case blocked[pair{a, b}]:
		return b
	case blocked[pair{b, a}]:
		return a
	default:
		return min(a, b)
	}
}

func (c config) neighbors() ([]Neighbor, error) {
	members := make(map[string]bool, len(c.Members))
	for _, m := range c.Members {
		if !memberID.MatchString(m) || members[m] {
			return nil, fmt.Errorf("invalid or duplicate member %q", m)
		}
		members[m] = true
	}
	if len(members) < 2 || !members[c.Local] {
		return nil, errors.New("members must include local and at least one other member")
	}
	if c.Parents != nil && c.Topology != "tree" {
		return nil, errors.New("parents requires a tree")
	}
	if (c.Edges != nil || c.MaxNeighbors != 0) && c.Topology != "partial-mesh" {
		return nil, errors.New("edges and maxNeighbors require a partial mesh")
	}
	if c.BlockedDials != nil && c.Topology != "full-mesh" && c.Topology != "partial-mesh" {
		return nil, errors.New("blockedDials requires a mesh")
	}
	blocked := map[pair]bool{}
	for _, raw := range c.BlockedDials {
		p, err := checkedPair(raw, members)
		if err != nil {
			return nil, fmt.Errorf("blockedDials: %w", err)
		}
		blocked[p] = true
	}

	local := []Neighbor{}
	add := func(a, b, dialer string) {
		switch c.Local {
		case a:
			local = append(local, Neighbor{b, dialer})
		case b:
			local = append(local, Neighbor{a, dialer})
		}
	}
	root := c.Members[0]
	switch c.Topology {
	case "point-to-point", "chain", "ring":
		if c.Topology == "point-to-point" && len(members) != 2 {
			return nil, errors.New("point-to-point requires two members")
		}
		if c.Topology == "ring" && len(members) < 3 {
			return nil, errors.New("a ring requires at least three members")
		}
		for i, m := range c.Members[1:] {
			add(c.Members[i], m, c.Members[i])
		}
		if c.Topology == "ring" {
			last := c.Members[len(c.Members)-1]
			add(last, root, last)
		}
	case "star":
		for _, m := range c.Members[1:] {
			add(m, root, m)
		}
	case "tree":
		if err := c.validateTree(members); err != nil {
			return nil, err
		}
		for child, parent := range c.Parents {
			add(child, parent, child)
		}
	case "full-mesh":
		for _, m := range c.Members {
			if m != c.Local {
				add(c.Local, m, meshDialer(c.Local, m, blocked))
			}
		}
	case "partial-mesh":
		if c.MaxNeighbors < 1 || len(c.Edges) == 0 {
			return nil, errors.New("partial-mesh requires priority-ordered edges and a positive maxNeighbors")
		}
		degree := map[string]int{}
		seen := map[pair]bool{}
		for _, raw := range c.Edges {
			p, err := checkedPair(raw, members)
			if err != nil {
				return nil, fmt.Errorf("edges: %w", err)
			}
			key := pair{min(p.from, p.to), max(p.from, p.to)}
			if seen[key] {
				return nil, fmt.Errorf("duplicate undirected edge %q", raw)
			}
			seen[key] = true
			owner := meshDialer(p.from, p.to, blocked)
			if owner == "" || degree[p.from] >= c.MaxNeighbors || degree[p.to] >= c.MaxNeighbors {
				continue
			}
			degree[p.from]++
			degree[p.to]++
			add(p.from, p.to, owner)
		}
	default:
		return nil, fmt.Errorf("unknown topology %q", c.Topology)
	}
	slices.SortFunc(local, func(a, b Neighbor) int { return strings.Compare(a.Member, b.Member) })
	return local, nil
}

func (c config) validateTree(members map[string]bool) error {
	root := c.Members[0]
	if _, ok := c.Parents[root]; ok || len(c.Parents) != len(members)-1 {
		return errors.New("each non-root member must have exactly one parent")
	}
	for child, parent := range c.Parents {
		if !members[child] || !members[parent] {
			return fmt.Errorf("unknown member in parent assignment %q -> %q", child, parent)
		}
	}
	// Follow each chain to the root or a previously checked chain.
	rooted := map[string]bool{root: true}
	for _, m := range c.Members {
		path := map[string]bool{}
		for !rooted[m] {
			if path[m] {
				return fmt.Errorf("parent cycle through %q", m)
			}
			path[m] = true
			m = c.Parents[m]
		}
		for p := range path {
			rooted[p] = true
		}
	}
	return nil
}
