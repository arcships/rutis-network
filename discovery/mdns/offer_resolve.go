package mdns

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"sync"
	"time"
)

type fetchKey struct {
	instance, srvTarget string
	port                uint16
	header              envelope
}

type fetchResult struct {
	description *Description
	address     string
}

type fetchState struct {
	done   chan fetchResult
	cancel context.CancelFunc
	tried  []string
	cached fetchResult
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
			c.Description = header.description([]byte(header.inline))
		} else {
			key := fetchKey{c.Instance, c.srvTarget, c.Port, *header}
			seen[key] = true
			st := r.pending[key]
			if st == nil {
				st = &fetchState{}
				r.pending[key] = st
			}
			c.Description = r.external(ctx, c, *header, st, now)
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

// A verified body belongs to its SRV endpoint, revision and the address
// that served it. Extra addresses do not invalidate that address's lease.
func (r *offerResolver) external(ctx context.Context, c Candidate, e envelope, st *fetchState, now time.Time) *Description {
	if st.cached.description != nil {
		if slices.Contains(c.Addresses, st.cached.address) {
			return st.cached.description
		}
		st.cached = fetchResult{}
	}
	if st.done != nil {
		select {
		case result := <-st.done:
			st.cancel()
			st.done = nil
			if result.description != nil && slices.Contains(c.Addresses, result.address) {
				st.cached = result
				return result.description
			}
			if slices.Equal(st.tried, c.Addresses) {
				st.retry = now.Add(3 * time.Second)
			}
		default:
			// Restart unfinished work if the set of DNS addresses changes.
			if slices.Equal(st.tried, c.Addresses) {
				return nil
			}
			st.cancel()
			st.done = nil
		}
	}
	if len(r.allowed) == 0 || now.Before(st.retry) && slices.Equal(st.tried, c.Addresses) {
		return nil
	}
	select {
	case r.slots <- struct{}{}:
		work, cancel := context.WithCancel(ctx)
		done := make(chan fetchResult, 1)
		st.cancel = cancel
		st.done = done
		st.tried = c.Addresses
		st.retry = time.Time{}
		r.workers.Go(func() { defer func() { <-r.slots }(); done <- r.fetch(work, c, e) })
	default:
	}
	return nil
}

func (r *offerResolver) fetch(ctx context.Context, c Candidate, e envelope) fetchResult {
	var addresses []netip.Addr
	for _, a := range c.Addresses {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			continue
		}
		raw := ip.WithZone("").Unmap()
		if e.listenIP.IsValid() && e.listenIP != raw {
			continue
		}
		if raw.IsMulticast() || raw.IsUnspecified() || raw.IsLoopback() && r.iface.Flags&net.FlagLoopback == 0 || ip.Zone() != "" && ip.Zone() != r.iface.Name {
			continue
		}
		if slices.ContainsFunc(r.allowed, func(p netip.Prefix) bool { return p.Contains(raw) }) {
			addresses = append(addresses, ip)
		}
	}
	d := net.Dialer{Control: interfaceControl(r.iface)}
	tr := &http.Transport{DialContext: d.DialContext, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 8192}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	for i, ip := range addresses {
		if ctx.Err() != nil {
			break
		}
		// Divide the remaining budget among eligible addresses. An immediate
		// refusal leaves the unused time for the rest of the candidates.
		attempt, finish := context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(addresses)-i))
		u := url.URL{Scheme: "http", Host: netip.AddrPortFrom(ip, c.Port).String(), Path: e.path()}
		desc := fetchParams(attempt, client, &u, e)
		finish()
		if desc != nil {
			return fetchResult{desc, ip.String()}
		}
	}
	return fetchResult{}
}

func fetchParams(ctx context.Context, client *http.Client, u *url.URL, e envelope) *Description {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil
	}
	res, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxParams+1))
	if err != nil || !e.validParams(body) {
		return nil
	}
	return e.description(body)
}
