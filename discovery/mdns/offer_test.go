package mdns

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"runtime"
	"slices"

	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcships/rutis/go/rutis"
	"github.com/arcships/rutis/go/rutis/rutistest"
	"github.com/miekg/dns"
)

func makeOffer(t *testing.T, id string, payload string) encodedOffer {
	t.Helper()
	params, err := json.Marshal(map[string]string{"value": payload})
	if err != nil {
		t.Fatal(err)
	}
	e, err := encodeOffer(Description{id, "example.custom", 7, params}, 1, netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// Decode DNS wire RDATA as the real mDNS receiver does.
func offerTXT(t *testing.T, instance string, txt []byte, ttl uint32) dns.RR {
	t.Helper()
	wire := append([]byte{0, 0, byte(dns.TypeTXT), 0, 1, 0, 0, 0, 0, byte(len(txt) >> 8), byte(len(txt))}, txt...)
	r, _, err := dns.UnpackRR(wire, 0)
	if err != nil {
		t.Fatal(err)
	}
	r.Header().Name = instance
	r.Header().Ttl = ttl
	return r
}

func TestGenericTXTLease(t *testing.T) {
	now := time.Now()
	cfg := config{Domain: "local.", Browse: offerType, Offers: &offerConfig{Accept: map[string][]int{"example.custom": {7}}}}
	cache := newCache("browser", cfg)
	fq := offerType + ".local."
	instance := "Custom." + fq
	e := makeOffer(t, "test", "节点 \"quoted\" \\ path")

	ptr := &dns.PTR{Hdr: header(fq, dns.TypePTR, 100), Ptr: instance}
	srv := &dns.SRV{Hdr: header(instance, dns.TypeSRV, 100), Port: 1234, Target: host}
	cache.ingest(response(ptr, srv, addr("192.0.2.8", 100)), now)
	list, q := cache.view(now)
	if _, asked := q[dns.Question{Name: dns.CanonicalName(instance), Qtype: dns.TypeTXT, Qclass: dns.ClassINET}]; len(list) != 0 || !asked {
		t.Fatal(list, q)
	}
	cache.ingest(response(offerTXT(t, instance, e.txt, 4)), now)
	list, _ = cache.view(now)
	if len(list) != 1 || list[0].ExpiresAt != now.Add(4*time.Second).UTC().Format(time.RFC3339Nano) {
		t.Fatal(list)
	}
	r := newResolver(nil, nil)
	got := r.resolve(t.Context(), list, now)
	if len(got) != 1 || !bytes.Equal(got[0].Description.Params, e.body) {
		t.Fatal(got)
	}
	if next, _ := cache.view(now.Add(4 * time.Second)); len(next) != 0 {
		t.Fatal("expired TXT survived")
	}
	cache.ingest(response(offerTXT(t, instance, e.txt, 30)), now)
	damaged := bytes.Replace(e.txt, []byte("seq=1"), []byte("seq=2"), 1)
	damaged[len(damaged)-3] ^= 1
	cache.ingest(response(offerTXT(t, instance, damaged, 20)), now.Add(time.Second))
	list, _ = cache.view(now.Add(time.Second))
	if got := r.resolve(t.Context(), list, now); len(got) != 0 {
		t.Fatal("corrupt revision replaced valid data")
	}
	cache.ingest(response(offerTXT(t, instance, bytes.Replace(e.txt, []byte("seq=1"), []byte("seq=3"), 1), 30)), now.Add(2*time.Second))
	cfg.Offers.Accept["example.custom"] = []int{1}
	if list, _ := cache.view(now.Add(2 * time.Second)); len(list) != 0 {
		t.Fatal("unsupported version")
	}
}

func TestOfferFetchPolicy(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("requires interface-bound TCP support")
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var loop *net.Interface
	for _, i := range ifaces {
		if i.Flags&net.FlagLoopback != 0 {
			loop = &i
			break
		}
	}
	if loop == nil {
		t.Skip("requires loopback")
	}
	e := makeOffer(t, "with-body", strings.Repeat("x", 450))
	header, ok := parseEnvelope(string(e.txt))
	if !ok || header.inline != "" {
		t.Fatal("expected external parameters")
	}
	var mode atomic.Int32
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != e.path() || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected request", r.URL)
			return
		}
		switch mode.Load() {
		case 0:
			_, _ = w.Write(e.body)
		case 1:
			_, _ = w.Write([]byte("{}"))
		case 2:
			http.Redirect(w, r, "/redirect", 302)
		case 3:
			_, _ = w.Write(bytes.Repeat([]byte{'x'}, maxParams+1))
		}
	}))
	defer server.Close()
	c := Candidate{Instance: "test", Addresses: []string{"127.0.0.1"}, Port: uint16(server.Listener.Addr().(*net.TCPAddr).Port), wire: header}
	r := newResolver(loop, nil)
	if got := r.fetch(t.Context(), c, *header); got.description != nil || hits.Load() != 0 {
		t.Fatal("fetched without local permission")
	}
	r.allowed = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	for i := 0; i < 4; i++ {
		mode.Store(int32(i))
		d := r.fetch(t.Context(), c, *header)
		if (d.description != nil) != (i == 0) || hits.Load() != int32(i+1) {
			t.Fatalf("mode %d, desc=%v, hits=%d", i, d, hits.Load())
		}
	}
	// A completed old fetch cannot reappear after an intervening withdrawal.
	mode.Store(0)
	if out := r.resolve(t.Context(), []Candidate{c}, time.Now()); len(out) != 0 {
		t.Fatal(out)
	}
	r.workers.Wait()
	if out := r.resolve(t.Context(), nil, time.Now()); len(out) != 0 || len(r.pending) != 0 {
		t.Fatal(out)
	}
}

// Exercise the same record-based RPC binding as a third-party source.
type liveOfferSource struct {
	mu      sync.Mutex
	cursor  int
	items   []any
	changed chan struct{}
}

func (s *liveOfferSource) Snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{"cursor": strconv.Itoa(s.cursor), "offers": s.items}
}
func (s *liveOfferSource) WaitForChange(ctx context.Context, cursor string) (map[string]any, error) {
	s.mu.Lock()
	changed := s.changed
	current := strconv.Itoa(s.cursor)
	s.mu.Unlock()
	if cursor == current {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
	return s.Snapshot(), nil
}
func (s *liveOfferSource) replace(items []any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = items
	s.cursor++
	close(s.changed)
	s.changed = make(chan struct{})
}

func TestLiveOfferRevisions(t *testing.T) {
	name := os.Getenv("MDNS_TEST_INTERFACE")
	if name == "" {
		t.Skip("set MDNS_TEST_INTERFACE")
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	source := &liveOfferSource{items: []any{}, changed: make(chan struct{})}
	// rutistest declares injected services on the definition; production rows
	// name the same dependency in their manifest.
	publisher := rutis.Define(rutis.Plugin[config]{Name: "test-offer-publisher", Inject: []string{"feed"}, Schema: schema(), Apply: apply})
	rutistest.Load(t, publisher, map[string]any{"interface": name, "offers": map[string]any{"source": "feed"}}, map[string]any{"feed": source})
	addrs, err := iface.Addrs()
	if err != nil {
		t.Fatal(err)
	}
	var cidrs []string
	for _, a := range addrs {
		p, err := netip.ParsePrefix(a.String())
		if err == nil {
			cidrs = append(cidrs, netip.PrefixFrom(p.Addr(), p.Addr().BitLen()).String())
		}
	}
	// On a dual-stack loopback require the real DNS-SD round trip to
	// obtain every external revision over IPv6 alone.
	if iface.Flags&net.FlagLoopback != 0 && slices.Contains(cidrs, "::1/128") {
		cidrs = []string{"::1/128"}
	}
	browser := rutistest.Load(t, Plugin, map[string]any{"interface": name, "service": "seen", "offers": map[string]any{"accept": map[string][]int{"example.custom": {7}}, "allowCIDRs": cidrs}}, nil)
	svc := browser.Service("seen")
	var state Snapshot
	if err := svc.Call(ctx, "snapshot", &state); err != nil {
		t.Fatal(err)
	}
	id := "live-" + rand.Text()[:10]
	wait := func(params []byte) {
		t.Helper()
		for {
			var found *Description
			for _, o := range state.Candidates {
				if o.Description.ID == id {
					found = o.Description
					break
				}
			}
			if found == nil && params == nil || found != nil && bytes.Equal(params, found.Params) {
				return
			}
			if err := svc.Call(ctx, "waitForChange", &state, state.Cursor); err != nil {
				t.Fatalf("waiting for %d bytes on %v: %v", len(params), cidrs, err)
			}
		}
	}
	for i, value := range []string{"small 节点", strings.Repeat("data", 150), "new"} {
		e := makeOffer(t, id, value)
		good := map[string]any{"id": id, "kind": e.kind, "version": e.version, "params": map[string]any{"value": value}}
		items := []any{good}
		if i > 0 {
			items = append(items, map[string]any{"id": "bad-sibling", "kind": e.kind, "version": "wrong type", "params": map[string]any{}})
		}
		source.replace(items)
		wait(e.body)
	}
	source.replace([]any{})
	wait(nil)
}
