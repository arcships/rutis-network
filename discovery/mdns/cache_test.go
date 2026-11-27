package mdns

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func testCache(iface string) *cache {
	return newCache("browse-"+iface, config{Interface: iface, Domain: "local.", Browse: "_rutis._udp"})
}

const fqdn = "_rutis._udp.local."
const instance = `Desk\.A._Rutis._UDP.local.`
const host = "device.local."

func header(name string, kind uint16, ttl uint32) dns.RR_Header {
	return dns.RR_Header{Name: name, Rrtype: kind, Class: dns.ClassINET, Ttl: ttl}
}
func ptr(ttl uint32) dns.RR { return &dns.PTR{Hdr: header(fqdn, dns.TypePTR, ttl), Ptr: instance} }
func srv(port uint16, ttl uint32) dns.RR {
	return &dns.SRV{Hdr: header(instance, dns.TypeSRV, ttl), Target: host, Port: port}
}
func addr(ip string, ttl uint32) dns.RR {
	return &dns.A{Hdr: header(host, dns.TypeA, ttl), A: net.ParseIP(ip).To4()}
}
func response(records ...dns.RR) *dns.Msg {
	return &dns.Msg{MsgHdr: dns.MsgHdr{Response: true, Authoritative: true}, Answer: records}
}

func TestResolveAndExpireAcrossPackets(t *testing.T) {
	t0 := time.Unix(100, 0)
	for _, iface := range []string{"lan0", "lan1"} {
		c := testCache(iface)
		c.ingest(response(ptr(20)), t0)
		list, questions := c.view(t0)
		if len(list) != 0 {
			t.Fatal(list)
		}
		if _, ok := questions[dns.Question{Name: dns.CanonicalName(instance), Qtype: dns.TypeSRV, Qclass: dns.ClassINET}]; !ok {
			t.Fatal("missing SRV question")
		}
		c.ingest(response(srv(1234, 15)), t0)
		_, questions = c.view(t0)
		for _, kind := range []uint16{dns.TypeA, dns.TypeAAAA} {
			if _, ok := questions[dns.Question{Name: host, Qtype: kind, Qclass: dns.ClassINET}]; !ok {
				t.Fatalf("missing %d question", kind)
			}
		}
		msg := response(addr("192.0.2.8", 3))
		msg.Extra = []dns.RR{&dns.AAAA{Hdr: header(host, dns.TypeAAAA, 10), AAAA: net.ParseIP("fe80::1")}}
		c.ingest(msg, t0)
		list, _ = c.view(t0)
		want := Candidate{Source: "browse-" + iface, Interface: iface, Domain: "local.", ServiceType: "_rutis._udp", Instance: "Desk.A", Transport: "udp",
			Addresses: []string{"192.0.2.8", "fe80::1%" + iface}, Port: 1234, ExpiresAt: t0.Add(3 * time.Second).UTC().Format(time.RFC3339Nano)}
		if !reflect.DeepEqual(list, []Candidate{want}) {
			t.Fatalf("got %+v, want %+v", list, want)
		}
		list, _ = c.view(t0.Add(3 * time.Second))
		if len(list) != 1 || !reflect.DeepEqual(list[0].Addresses, want.Addresses[1:]) {
			t.Fatal(list)
		}
		list, _ = c.view(t0.Add(10 * time.Second))
		if len(list) != 0 {
			t.Fatal(list)
		}
	}
}

func TestGoodbyeRescueAndCacheFlush(t *testing.T) {
	c := testCache("lan0")
	t0 := time.Unix(100, 0)
	c.ingest(response(ptr(120), srv(1234, 120), addr("192.0.2.1", 120)), t0)
	change := t0.Add(2 * time.Second)
	replaced := addr("192.0.2.2", 120)
	replaced.Header().Class |= 0x8000
	port := srv(4321, 120)
	port.Header().Class |= 0x8000
	c.ingest(response(replaced, port, ptr(0)), change)
	// A second packet with another cache-flush address retains its sibling.
	other := addr("192.0.2.3", 120)
	other.Header().Class |= 0x8000
	c.ingest(response(other, ptr(120)), change.Add(200*time.Millisecond))
	list, _ := c.view(change.Add(time.Second))
	if len(list) != 1 || list[0].Port != 4321 || !reflect.DeepEqual(list[0].Addresses, []string{"192.0.2.2", "192.0.2.3"}) {
		t.Fatal(list)
	}
	c.ingest(response(ptr(0)), change.Add(time.Second))
	list, _ = c.view(change.Add(2 * time.Second))
	if len(list) != 0 {
		t.Fatal(list)
	}
}

func TestRecordsNeedLivePTRAndSRV(t *testing.T) {
	t0 := time.Unix(100, 0)
	for _, rrs := range [][]dns.RR{{ptr(1), srv(80, 30), addr("192.0.2.1", 30)}, {ptr(30), srv(80, 1), addr("192.0.2.1", 30)}} {
		c := testCache("lo")
		query := response(rrs...)
		query.Response = false
		c.ingest(query, t0)
		list, _ := c.view(t0)
		if len(list) != 0 {
			t.Fatal("cached a query", list)
		}
		c.ingest(response(rrs...), t0)
		list, _ = c.view(t0.Add(time.Second))
		if len(list) != 0 {
			t.Fatal("stale result", list)
		}
	}
}

func TestKnownAnswersAndScope(t *testing.T) {
	c := testCache("lan0")
	t0 := time.Unix(100, 0)
	stray := addr("192.0.2.99", 120)
	stray.Header().Name = "unrelated.local."
	c.ingest(response(stray, addr("192.0.2.1", 120), srv(8080, 120), ptr(120)), t0)
	if len(c.records) != 3 {
		t.Fatal(c.records)
	}
	q := []dns.Question{{Name: fqdn, Qtype: dns.TypePTR, Qclass: dns.ClassINET}}
	fresh := c.query(q, t0.Add(time.Second)).Answer
	if len(fresh) != 1 || fresh[0].Header().Ttl != 119 || fresh[0].Header().Class != dns.ClassINET {
		t.Fatal(fresh)
	}
	if answers := c.query(q, t0.Add(61*time.Second)).Answer; len(answers) != 0 {
		t.Fatal(answers)
	}
	offline := srv(0, 120).(*dns.SRV)
	offline.Target = "."
	offline.Hdr.Class |= 0x8000
	c.ingest(response(offline), t0.Add(time.Minute))
	if list, _ := c.view(t0.Add(time.Minute)); len(list) != 0 {
		t.Fatal(list)
	}
}

func TestCursorsAndCancellation(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	s := newObservations(ctx)
	old, _ := s.Snapshot()
	result := make(chan Snapshot, 1)
	go func() { v, _ := s.WaitForChange(ctx, old.Cursor); result <- v }()
	s.update([]Candidate{{Instance: "A", Addresses: []string{"192.0.2.1"}}})
	got := <-result
	if got.Cursor == old.Cursor || len(got.Candidates) != 1 {
		t.Fatal(got)
	}
	got.Candidates[0].Addresses[0] = "changed"
	got, _ = s.Snapshot()
	if got.Candidates[0].Addresses[0] != "192.0.2.1" {
		t.Fatal("caller mutated shared state")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.WaitForChange(canceled, got.Cursor); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	fresh := newObservations(ctx)
	reloaded, err := fresh.WaitForChange(ctx, got.Cursor)
	if err != nil || reloaded.Cursor == got.Cursor || len(reloaded.Candidates) != 0 {
		t.Fatal(reloaded, err)
	}
	stop()
	if _, err := s.WaitForChange(context.Background(), got.Cursor); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestConfiguration(t *testing.T) {
	for _, c := range []config{
		{Interface: "lo"}, {Interface: "lo", Browse: "_rutis._tcp"},
		{Interface: "lo", Service: "lan", Browse: "_no--dash._tcp"},
		{Interface: "lo", Service: "lan", Browse: "_123._tcp"},
		{Interface: "lo", Service: "rutis/sessions", Browse: "_rutis._tcp"},
		{Interface: "lo", Service: "lan", Browse: "_rutis._tcp", Domain: "other."},
		{Interface: "lo", Publish: &advert{Type: "_rutis._tcp", Port: 65536, Instance: "A"}},
		{Interface: "lo", Publish: &advert{Type: "_rutis._tcp", Port: 443, Instance: strings.Repeat("界", 22)}},
	} {
		if err := c.validate(); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	c := config{Interface: "lo", Service: "lan", Browse: "_rutis._tcp", Publish: &advert{Type: "_rutis._udp", Instance: "A.B", Port: 443}}
	if err := c.validate(); err != nil || c.Domain != "local." {
		t.Fatal(c, err)
	}
}
