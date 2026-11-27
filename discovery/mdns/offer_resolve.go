package mdns

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

type fetchKey struct {
	instance, addresses string
	port                uint16
	header              envelope
}

type fetchState struct {
	done   chan *Description
	cancel context.CancelFunc
	cached *Description
	retry  time.Time
}

type offerResolver struct {
	iface   *net.Interface
	allowed []netip.Prefix
	pending map[fetchKey]*fetchState
	slots   chan struct{}
	workers sync.WaitGroup
}

func newResolver(iface *net.Interface, allowed []netip.Prefix) *offerResolver {
	return &offerResolver{iface: iface, allowed: allowed, pending: map[fetchKey]*fetchState{}, slots: make(chan struct{}, 4)}
}

func (r *offerResolver) resolve(ctx context.Context, list []Candidate, now time.Time) []Candidate {
	out := make([]Candidate, 0, len(list))
	seen := map[fetchKey]bool{}
	for _, c := range list {
		header := c.wire
		c.wire = nil
		if header.inline != "" {
			c.Description, _ = header.decode([]byte(header.inline))
		} else {
			key := fetchKey{c.Instance, strings.Join(c.Addresses, ","), c.Port, *header}
			seen[key] = true
			st := r.pending[key]
			if st == nil {
				st = &fetchState{}
				r.pending[key] = st
			}
			if st.done != nil {
				select {
				case d := <-st.done:
					st.cached = d
					st.done = nil
					st.cancel()
					st.retry = now.Add(3 * time.Second)
				default:
				}
			}
			if st.cached == nil && st.done == nil && len(r.allowed) > 0 && !now.Before(st.retry) {
				select {
				case r.slots <- struct{}{}:
					work, cancel := context.WithTimeout(ctx, 3*time.Second)
					done := make(chan *Description, 1)
					st.cancel = cancel
					st.done = done
					candidate := c
					r.workers.Go(func() { defer func() { <-r.slots }(); done <- r.fetch(work, candidate, *header) })
				default:
				}
			}
			c.Description = st.cached
		}
		if c.Description != nil {
			out = append(out, c)
		}
	}
	for k, st := range r.pending {
		if !seen[k] {
			if st.cancel != nil {
				st.cancel()
			}
			delete(r.pending, k)
		}
	}
	return out
}

func (r *offerResolver) fetch(ctx context.Context, c Candidate, e envelope) *Description {
	d := net.Dialer{Control: interfaceControl(r.iface)}
	tr := &http.Transport{DialContext: d.DialContext, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 8192}
	defer tr.CloseIdleConnections()
	client := http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, a := range c.Addresses {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			continue
		}
		raw := ip.WithZone("").Unmap()
		if raw.IsMulticast() || raw.IsUnspecified() || raw.IsLoopback() && r.iface.Flags&net.FlagLoopback == 0 || ip.Zone() != "" && ip.Zone() != r.iface.Name {
			continue
		}
		if !slices.ContainsFunc(r.allowed, func(p netip.Prefix) bool { return p.Contains(raw) }) {
			continue
		}
		u := url.URL{Scheme: "http", Host: netip.AddrPortFrom(ip, c.Port).String(), Path: e.path()}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			continue
		}
		res, err := client.Do(req)
		if err != nil {
			continue
		}
		var desc *Description
		if res.StatusCode == http.StatusOK {
			body, err := io.ReadAll(io.LimitReader(res.Body, maxParams+1))
			if err == nil && len(body) <= maxParams {
				desc, _ = e.decode(body)
			}
		}
		res.Body.Close()
		if desc != nil {
			return desc
		}
	}
	return nil
}
