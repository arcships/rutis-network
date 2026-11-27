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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcships/rutis/go/rutis/rutistest"
	"github.com/miekg/dns"
)

func makeOffer(t *testing.T, id string, payload string) encodedOffer {
	t.Helper()
	params, err := json.Marshal(map[string]string{"value": payload})
	if err != nil {
		t.Fatal(err)
	}
	e, err := encodeOffer(Description{id, "example.custom", 7, params})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestGenericTXTLease(t *testing.T) {
	now := time.Now()
	cfg := config{Domain: "local.", Browse: offerType, Offers: &offerConfig{Accept: map[string][]int{"example.custom": {7}}}}
	cache := newCache("browser", cfg)
	fq := offerType + ".local."
	instance := "Custom." + fq
	e := makeOffer(t, "test", "节点 \"quoted\" \\ path")
	// Decode DNS wire RDATA as the real mDNS receiver does.
	rr := func(txt []byte, ttl uint32) dns.RR {
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
	ptr := &dns.PTR{Hdr: header(fq, dns.TypePTR, 100), Ptr: instance}
	srv := &dns.SRV{Hdr: header(instance, dns.TypeSRV, 100), Port: 1234, Target: host}
	cache.ingest(response(ptr, srv, addr("192.0.2.8", 100)), now)
	list, q := cache.view(now)
	if _, asked := q[dns.Question{Name: dns.CanonicalName(instance), Qtype: dns.TypeTXT, Qclass: dns.ClassINET}]; len(list) != 0 || !asked {
		t.Fatal(list, q)
	}
	cache.ingest(response(rr(e.txt, 4)), now)
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
	cache.ingest(response(rr(e.txt, 30)), now)
	damaged := bytes.Clone(e.txt)
	damaged[len(damaged)-3] ^= 1
	cache.ingest(response(rr(damaged, 20)), now.Add(time.Second))
	list, _ = cache.view(now.Add(time.Second))
	if got := r.resolve(t.Context(), list, now); len(got) != 0 {
		t.Fatal("corrupt revision replaced valid data")
	}
	cache.ingest(response(rr(e.txt, 30)), now.Add(2*time.Second))
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
	if got := r.fetch(t.Context(), c, *header); got != nil || hits.Load() != 0 {
		t.Fatal("fetched without local permission")
	}
	r.allowed = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	for i := 0; i < 4; i++ {
		mode.Store(int32(i))
		d := r.fetch(t.Context(), c, *header)
		if (d != nil) != (i == 0) || hits.Load() != int32(i+1) {
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
	l, err := offerListener(ctx, iface, "")
	if err != nil {
		t.Fatal(err)
	}
	c := &catalogue{bodies: map[string][]byte{}}
	server := &http.Server{Handler: c}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(l) }()
	defer server.Close()
	items := make(chan []Description)
	run, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- c.publish(run, items, serverErr, iface.Index, l.Addr().(*net.TCPAddr).Port) }()
	defer func() { stop(); <-done }()
	nameIP := l.Addr().(*net.TCPAddr).AddrPort().Addr().WithZone("")
	cidr := nameIP.String() + "/" + strconv.Itoa(nameIP.BitLen())
	browser := rutistest.Load(t, Plugin, map[string]any{"interface": name, "service": "seen", "offers": map[string]any{"accept": map[string][]int{"example.custom": {7}}, "allowCIDRs": []string{cidr}}}, nil)
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
				t.Fatalf("waiting for %d bytes on %s: %v", len(params), cidr, err)
			}
		}
	}
	for _, value := range []string{"small 节点", strings.Repeat("data", 150), "new"} {
		e := makeOffer(t, id, value)
		select {
		case items <- []Description{{id, e.kind, e.version, e.body}}:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		wait(e.body)
	}
	select {
	case items <- nil:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	wait(nil)
}
