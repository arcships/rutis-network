package mdns

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/miekg/dns"
)

func TestOfferSequenceRejectsDelayedAnnouncements(t *testing.T) {
	now := time.Now()
	c := newCache("browser", config{Domain: "local.", Browse: offerType, Offers: &offerConfig{Accept: map[string][]int{"rutis.tcp": {1}}}})
	instance := "listener." + c.fqdn
	c.ingest(response(&dns.PTR{Hdr: header(c.fqdn, dns.TypePTR, 90), Ptr: instance}, &dns.SRV{Hdr: header(instance, dns.TypeSRV, 90), Target: host, Port: 9999}, addr("192.0.2.1", 90)), now)
	portA := json.RawMessage(`{"host":"192.0.2.1","port":7443}`)
	portB := json.RawMessage(`{"host":"192.0.2.1","port":8443}`)
	encode := func(params json.RawMessage, seq uint64) []byte {
		t.Helper()
		e, err := encodeOffer(Description{"office", "rutis.tcp", 1, params}, seq, netip.Addr{})
		if err != nil {
			t.Fatal(err)
		}
		return e.txt
	}
	a, b := encode(portA, 1), encode(portB, 2)
	// A pre-sequence v1 instance is still readable until the first sequenced update.
	old := bytes.Replace(a, []byte("\x05seq=1"), nil, 1)
	resolver := newResolver(nil, nil)
	check := func(txt []byte, ttl uint32, at time.Time, want json.RawMessage) {
		t.Helper()
		c.ingest(response(offerTXT(t, instance, txt, ttl)), at)
		view, _ := c.view(at)
		got := resolver.resolve(t.Context(), view, at)
		if len(got) != 1 || !bytes.Equal(got[0].Description.Params, want) {
			t.Fatalf("got %+v; want %s", got, want)
		}
	}
	check(old, 30, now, portA)
	check(a, 30, now, portA)
	check(b, 20, now.Add(time.Second), portB)
	check(a, 90, now.Add(2*time.Second), portB)
	check(a, 0, now.Add(3*time.Second), portB)
	check(old, 90, now.Add(4*time.Second), portB)
	check(encode(portA, 2), 90, now.Add(5*time.Second), portB)
	// Rejected packets must not extend the accepted lease.
	view, _ := c.view(now.Add(6 * time.Second))
	if len(view) != 1 || view[0].ExpiresAt != now.Add(21*time.Second).UTC().Format(time.RFC3339Nano) {
		t.Fatal(view)
	}
	// A publisher may deliberately return to an earlier destination.
	if view, _ := c.view(now.Add(22 * time.Second)); len(view) != 0 {
		t.Fatal("exposed expired TXT", view)
	}
	c.ingest(response(offerTXT(t, instance, a, 90)), now.Add(23*time.Second))
	if view, _ := c.view(now.Add(23 * time.Second)); len(view) != 0 {
		t.Fatal("resurrected old TXT under a live PTR", view)
	}
	check(encode(portA, 3), 20, now.Add(24*time.Second), portA)
}

func TestMalformedAndDuplicateOffersAreIsolated(t *testing.T) {
	var record any
	if err := json.Unmarshal([]byte(`{"cursor":"2","offers":[
  {"id":"dup","kind":"other","version":"oops","params":{}},
  {"id":"ok","kind":"rutis.tcp","version":1,"params":{"port":8443}},
  {"id":"dup","kind":"other","version":1,"params":{}},
  {"id":"bad","kind":"other","version":1,"params":null},
  {"id":"wrong-version","kind":"other","version":"one","params":{}},
  17
 ]}`), &record); err != nil {
		t.Fatal(err)
	}
	s, err := decodeSnapshot(record)
	if err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		desired := snapshotOffers(s.Offers, 2, netip.Addr{})
		if len(desired) != 1 || string(desired["ok"].body) != `{"port":8443}` {
			t.Fatalf("unrelated or ambiguous offer survived: %v", desired)
		}
	}
	check()
	slices.Reverse(s.Offers)
	check()
	for _, raw := range []any{nil, map[string]any{"cursor": "3", "offers": "broken"}, map[string]any{"cursor": "3", "offers": make([]any, maxOffers+1)}} {
		if _, err := decodeSnapshot(raw); err == nil {
			t.Fatalf("accepted invalid full snapshot: %T", raw)
		}
	}
	empty, err := decodeSnapshot(map[string]any{"cursor": "4", "offers": []any{}})
	if err != nil || len(empty.Offers) != 0 {
		t.Fatal(empty, err)
	}
}

func TestMalformedSnapshotKeepsLastViewUntilExplicitWithdrawal(t *testing.T) {
	wireEmpty := map[string]any{"cursor": "3", "offers": []any{}}
	for _, tc := range []struct {
		name             string
		malformed, empty any
	}{
		{"native null", SourceSnapshot{Cursor: "2"}, SourceSnapshot{Cursor: "3", Offers: []Description{}}},
		{"wire null", map[string]any{"cursor": "2", "offers": nil}, wireEmpty},
		{"typed nil list", map[string]any{"cursor": "2", "offers": []any(nil)}, wireEmpty},
		{"wrong type", map[string]any{"cursor": "2", "offers": "malformed"}, wireEmpty},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkSnapshotRecovery(t, tc.malformed, tc.empty)
		})
	}
}

func checkSnapshotRecovery(t *testing.T, malformed, empty any) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		calls := 0
		source := offerSource{
			Snapshot: func(context.Context) (any, error) {
				calls++
				if calls == 1 {
					return SourceSnapshot{Cursor: "1", Offers: []Description{{ID: "keep"}}}, nil
				}
				return empty, nil
			},
			WaitForChange: func(ctx context.Context, cursor string) (any, error) {
				if cursor == "1" {
					return malformed, nil
				}
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}
		updates := make(chan []Description, 3)
		go readSource(ctx, source, updates)
		if items := <-updates; len(items) != 1 || items[0].ID != "keep" {
			t.Fatal(items)
		}
		synctest.Wait()
		select {
		case items := <-updates:
			t.Fatalf("invalid snapshot replaced previous offers: %v", items)
		default:
		}
		time.Sleep(2 * time.Second)
		if items := <-updates; items == nil || len(items) != 0 {
			t.Fatalf("expected explicit empty snapshot, got %#v", items)
		}
	})
}

// Run the production listener setup on loopback, with a controlled HTTP response.
func serveOfferTest(t *testing.T, pin netip.Addr, h http.HandlerFunc) (*net.Interface, uint16) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("requires interface-bound HTTP")
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback == 0 {
			continue
		}
		listeners, err := offerListeners(t.Context(), &iface, pin)
		if err != nil {
			t.Fatal(err)
		}
		port := listeners[0].Addr().(*net.TCPAddr).AddrPort().Port()
		server := &http.Server{Handler: h}
		var workers sync.WaitGroup
		for _, l := range listeners {
			if l.Addr().(*net.TCPAddr).AddrPort().Port() != port {
				t.Fatal("different SRV ports")
			}
			workers.Go(func() { _ = server.Serve(l) })
		}
		t.Cleanup(func() { server.Close(); workers.Wait() })
		return &iface, port
	}
	t.Skip("requires loopback")
	return nil, 0
}

func requireV6Loopback(t *testing.T, iface *net.Interface) {
	t.Helper()
	addrs, err := iface.Addrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr() == netip.IPv6Loopback() {
			return
		}
	}
	t.Skip("requires IPv6 on loopback")
}

func TestVerifiedOfferSurvivesUnrelatedAddressChanges(t *testing.T) {
	e := makeOffer(t, "office", strings.Repeat("x", 500))
	var hits atomic.Int32
	iface, port := serveOfferTest(t, netip.Addr{}, func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); _, _ = w.Write(e.body) })
	header, _ := parseEnvelope(string(e.txt))
	r := newResolver(iface, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
	c := Candidate{Instance: "office", srvTarget: "host.local.", Port: port, Addresses: []string{"127.0.0.1"}, wire: header}
	resolve := func() []Candidate { return r.resolve(t.Context(), []Candidate{c}, time.Now()) }
	defer r.workers.Wait()
	resolve()
	r.workers.Wait()
	for _, ips := range [][]string{{"127.0.0.1"}, {"127.0.0.1", "::1"}, {"127.0.0.1"}} {
		c.Addresses = ips
		got := resolve()
		if len(got) != 1 || !bytes.Equal(got[0].Description.Params, e.body) || hits.Load() != 1 {
			t.Fatalf("addresses %v lost the verified offer or refetched it: %v, %d requests", ips, got, hits.Load())
		}
	}
	c.Addresses = []string{"::1"}
	if got := resolve(); len(got) != 0 {
		t.Fatal("kept a body after its serving address expired", got)
	}
}

func TestLargeOfferOverBothAddressFamilies(t *testing.T) {
	e := makeOffer(t, "office", strings.Repeat("x", 500))
	iface, port := serveOfferTest(t, netip.Addr{}, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(e.body) })
	requireV6Loopback(t, iface)
	c := Candidate{Port: port, Addresses: []string{"127.0.0.1", "::1"}}
	for _, cidr := range []string{"127.0.0.1/32", "::1/128"} {
		prefix := netip.MustParsePrefix(cidr)
		r := newResolver(iface, []netip.Prefix{prefix})
		got := r.fetch(t.Context(), c, e.envelope)
		if got.description == nil || got.address != prefix.Addr().String() {
			t.Fatal("unreachable SRV address", cidr, got.address)
		}
	}
}

func TestPinnedHTTPAddressRequiresDNSAndLocalPermission(t *testing.T) {
	pin := netip.MustParseAddr("127.0.0.1")
	e, err := encodeOffer(Description{"office", "example.custom", 1, json.RawMessage(`{"value":"` + strings.Repeat("x", 500) + `"}`)}, 1, pin)
	if err != nil {
		t.Fatal(err)
	}
	header, ok := parseEnvelope(string(e.txt))
	if !ok || header.listenIP != pin || header.inline != "" {
		t.Fatal(header)
	}
	var hits atomic.Int32
	iface, port := serveOfferTest(t, pin, func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); _, _ = w.Write(e.body) })
	c := Candidate{Port: port, Addresses: []string{"::1"}}
	r := newResolver(iface, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
	if got := r.fetch(t.Context(), c, *header); got.description != nil {
		t.Fatal("hint without DNS address was fetched")
	}
	c.Addresses = append(c.Addresses, pin.String())
	r.allowed = nil
	if got := r.fetch(t.Context(), c, *header); got.description != nil {
		t.Fatal("hint bypassed local address policy")
	}
	r.allowed = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	if got := r.fetch(t.Context(), c, *header); got.description == nil || got.address != pin.String() || hits.Load() != 1 {
		t.Fatal("pinned listener not resolved", got, hits.Load())
	}
}

func TestSlowFirstAddressLeavesTimeForFallback(t *testing.T) {
	e := makeOffer(t, "fallback", strings.Repeat("x", 500))
	var slow, fast atomic.Int32
	iface, port := serveOfferTest(t, netip.Addr{}, func(w http.ResponseWriter, req *http.Request) {
		local := req.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
		if local.IP.To4() != nil {
			slow.Add(1)
			<-req.Context().Done()
			return
		}
		fast.Add(1)
		_, _ = w.Write(e.body)
	})
	requireV6Loopback(t, iface)
	r := newResolver(iface, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")})
	ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
	defer cancel()
	got := r.fetch(ctx, Candidate{Port: port, Addresses: []string{"127.0.0.1", "::1"}}, e.envelope)
	if got.description == nil || got.address != "::1" || slow.Load() != 1 || fast.Load() != 1 {
		t.Fatalf("failed to use secondary before deadline: %v; requests %d/%d", got, slow.Load(), fast.Load())
	}
}
