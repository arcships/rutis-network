package mdns

import (
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/miekg/dns"
)

// Candidate describes a resolved DNS-SD instance as observed on one interface.
type Candidate struct {
	Description *Description `json:"description,omitempty"`
	wire        *envelope
	Source      string   `json:"source"`
	Interface   string   `json:"interface"`
	Domain      string   `json:"domain"`
	ServiceType string   `json:"serviceType"`
	Instance    string   `json:"instance"`
	Transport   string   `json:"transport"`
	Addresses   []string `json:"addresses"`
	Port        uint16   `json:"port"`
	ExpiresAt   string   `json:"expiresAt"`
}

type key struct {
	name  string
	kind  uint16
	value string
	port  uint16
}

type lease struct {
	txtData                string
	wire                   *envelope
	instance               string
	seen, expires, refresh time.Time
	rr                     dns.RR
}

type cache struct {
	source  string
	cfg     config
	fqdn    string
	records map[key]lease
}

func newCache(source string, cfg config) *cache {
	return &cache{source: source, cfg: cfg, fqdn: cfg.Browse + "." + cfg.Domain, records: make(map[key]lease)}
}

// Parse the first DNS label from its wire encoding, including escaped dots,
// spaces and UTF-8 octets in a service instance.
func (c *cache) instance(name string) (string, bool) {
	rest, end := dns.NextLabel(name, 0)
	if end || !strings.EqualFold(name[rest:], c.fqdn) {
		return "", false
	}
	var wire [256]byte
	n, err := dns.PackDomainName(name, wire[:], 0, nil, false)
	if err != nil || n < 2 || wire[0] == 0 {
		return "", false
	}
	label := string(wire[1 : 1+int(wire[0])])
	return label, utf8.ValidString(label) && strings.IndexFunc(label, unicode.IsControl) < 0
}

func (c *cache) key(rr dns.RR) (key, string, bool) {
	h := rr.Header()
	k := key{name: dns.CanonicalName(h.Name), kind: h.Rrtype}
	if h.Class&0x7fff != dns.ClassINET {
		return k, "", false
	}
	switch r := rr.(type) {
	case *dns.PTR:
		if k.name != c.fqdn {
			return k, "", false
		}
		k.value = dns.CanonicalName(r.Ptr)
		display, ok := c.instance(r.Ptr)
		return k, display, ok
	case *dns.TXT:
		if _, ok := c.instance(h.Name); !ok || c.cfg.Offers == nil {
			return k, "", false
		}
		// Pack the RDATA to recover original bytes from DNS presentation escapes.
		copyRR := *r
		copyRR.Hdr.Name = "."
		var buf [11 + maxTXT]byte
		n, err := dns.PackRR(&copyRR, buf[:], 0, nil, false)
		if err == nil {
			k.value = string(buf[11:n])
		}
	case *dns.SRV:
		if _, ok := c.instance(r.Hdr.Name); !ok {
			return k, "", false
		}
		k.value = dns.CanonicalName(r.Target)
		k.port = r.Port
	case *dns.A, *dns.AAAA:
		if !dns.IsSubDomain(c.cfg.Domain, k.name) {
			return k, "", false
		}
		var addr netip.Addr
		if a, ok := rr.(*dns.A); ok {
			addr, _ = netip.AddrFromSlice(a.A.To4())
		} else {
			addr, _ = netip.AddrFromSlice(rr.(*dns.AAAA).AAAA)
			if !addr.Is6() || addr.Is4In6() {
				return k, "", false
			}
		}
		if !addr.IsValid() || addr.IsUnspecified() || addr.IsMulticast() {
			return k, "", false
		}
		if addr.Is6() && addr.IsLinkLocalUnicast() {
			addr = addr.WithZone(c.cfg.Interface)
		}
		k.value = addr.String()
	default:
		return k, "", false
	}
	return k, "", true
}

const maxRecords = 4096

func (c *cache) ingest(msg *dns.Msg, now time.Time) {
	if !msg.Response || msg.Opcode != dns.OpcodeQuery || msg.Rcode != dns.RcodeSuccess {
		return
	}
	// RFC 6762 sections 10.1–2: a goodbye or an old member of a flushed RRSet
	// gets one second for a correcting announcement. Siblings received in
	// the past second can be part of the same multi-packet announcement.
	type rrset struct {
		name string
		kind uint16
	}
	flushed := map[rrset]bool{}
	// A and AAAA are shared by all mDNS services. Keep only addresses for
	// targets of this type, including SRV records later in the same packet.
	targets := map[string]bool{}
	for k := range c.records {
		if k.kind == dns.TypeSRV {
			targets[k.value] = true
		}
	}
	for _, section := range [][]dns.RR{msg.Answer, msg.Extra} {
		for _, rr := range section {
			if srv, ok := rr.(*dns.SRV); ok {
				if k, _, ok := c.key(srv); ok {
					targets[k.value] = true
				}
			}
		}
	}
	grace := now.Add(time.Second)
	for _, section := range [][]dns.RR{msg.Answer, msg.Extra} {
		for _, rr := range section {
			k, display, ok := c.key(rr)
			if !ok {
				continue
			}
			if (k.kind == dns.TypeA || k.kind == dns.TypeAAAA) && !targets[k.name] {
				continue
			}
			h := rr.Header()
			// A TXT RR is unique for each service instance. Use one slot even if
			// the cache is full, but only apply a goodbye to the same RDATA.
			var txtData string
			if k.kind == dns.TypeTXT {
				txtData, k.value = k.value, ""
			}
			old, known := c.records[k]
			if h.Ttl == 0 {
				if known && old.txtData == txtData && old.expires.After(grace) {
					old.expires = grace
					c.records[k] = old
				}
				continue
			}
			if !known && len(c.records) >= maxRecords {
				continue
			}
			life := time.Duration(h.Ttl) * time.Second
			var wire *envelope
			if k.kind == dns.TypeTXT {
				wire, _ = parseEnvelope(txtData)
			}
			c.records[k] = lease{txtData: txtData, wire: wire, instance: display, seen: now, expires: now.Add(life), refresh: now.Add(life - life/5), rr: rr}
			if h.Class&0x8000 != 0 && k.kind != dns.TypePTR {
				flushed[rrset{k.name, k.kind}] = true
			}
		}
	}
	if len(flushed) > 0 {
		for k, v := range c.records {
			if flushed[rrset{k.name, k.kind}] && now.Sub(v.seen) >= time.Second && v.expires.After(grace) {
				v.expires = grace
				c.records[k] = v
			}
		}
	}
}

// Include fresh known answers that fit in the question datagram. This lets
// responders suppress replies for instances we already know (RFC 6762 §7.1).
func (c *cache) query(questions []dns.Question, now time.Time) *dns.Msg {
	msg := &dns.Msg{Question: questions, Compress: true}
	size := msg.Len()
	for k, l := range c.records {
		left := l.expires.Sub(now)
		if left < time.Second || left < time.Duration(l.rr.Header().Ttl)*time.Second/2 {
			continue
		}
		for _, q := range questions {
			if k.kind != q.Qtype || k.name != q.Name {
				continue
			}
			n := dns.Len(l.rr)
			if size+n > 1232 {
				break
			}
			rr := dns.Copy(l.rr)
			rr.Header().Class = dns.ClassINET
			rr.Header().Ttl = uint32(left / time.Second)
			msg.Answer = append(msg.Answer, rr)
			size += n
			break
		}
	}
	return msg
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Build a consistent view from separately leased PTR, SRV and A/AAAA data.
func (c *cache) view(now time.Time) ([]Candidate, map[dns.Question]bool) {
	ptrs := map[string]lease{}
	txts := map[string]lease{}
	srvs := map[string]key{}
	addrs := map[string][]key{}
	questions := map[dns.Question]bool{}
	ask := func(name string, kind uint16, refresh bool) {
		q := dns.Question{Name: name, Qtype: kind, Qclass: dns.ClassINET}
		questions[q] = questions[q] || refresh
	}
	ask(c.fqdn, dns.TypePTR, false)
	for k, v := range c.records {
		if !v.expires.After(now) {
			delete(c.records, k)
			continue
		}
		switch k.kind {
		case dns.TypeTXT:
			txts[k.name] = v
		case dns.TypePTR:
			ptrs[k.value] = v
			if !v.refresh.After(now) {
				ask(c.fqdn, dns.TypePTR, true)
			}
		case dns.TypeSRV:
			prev, ok := srvs[k.name]
			if !ok || v.seen.After(c.records[prev].seen) || (v.seen.Equal(c.records[prev].seen) && (k.value < prev.value || k.value == prev.value && k.port < prev.port)) {
				srvs[k.name] = k
			}
		case dns.TypeA, dns.TypeAAAA:
			addrs[k.name] = append(addrs[k.name], k)
		}
	}
	out := []Candidate{}
	for instance, ptr := range ptrs {
		txt := txts[instance]
		if c.cfg.Offers != nil && !txt.refresh.After(now) {
			ask(instance, dns.TypeTXT, !txt.seen.IsZero())
		}
		srv, ok := srvs[instance]
		if !ok || !c.records[srv].refresh.After(now) {
			ask(instance, dns.TypeSRV, ok)
		}
		if !ok || srv.port == 0 || !dns.IsSubDomain(c.cfg.Domain, srv.value) || srv.value == c.cfg.Domain {
			continue
		}
		addresses := addrs[srv.value]
		until := earlier(ptr.expires, c.records[srv].expires)
		if c.cfg.Offers != nil {
			if txt.wire == nil || !slices.Contains(c.cfg.Offers.Accept[txt.wire.kind], txt.wire.version) {
				continue
			}
			until = earlier(until, txt.expires)
		}
		ips := make([]string, 0, len(addresses))
		var hasA, hasAAAA bool
		for _, a := range addresses {
			v := c.records[a]
			hasA = hasA || a.kind == dns.TypeA
			hasAAAA = hasAAAA || a.kind == dns.TypeAAAA
			ips = append(ips, a.value)
			until = earlier(until, v.expires)
			if !v.refresh.After(now) {
				ask(a.name, a.kind, true)
			}
		}
		if !hasA {
			ask(srv.value, dns.TypeA, false)
		}
		if !hasAAAA {
			ask(srv.value, dns.TypeAAAA, false)
		}
		if len(ips) == 0 {
			continue
		}
		sort.Strings(ips)
		_, transport, _ := strings.Cut(c.cfg.Browse, "._")
		out = append(out, Candidate{wire: txt.wire, Source: c.source, Interface: c.cfg.Interface, Domain: c.cfg.Domain, ServiceType: c.cfg.Browse,
			Instance: ptr.instance, Transport: transport, Addresses: ips, Port: srv.port, ExpiresAt: until.UTC().Format(time.RFC3339Nano)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instance < out[j].Instance })
	return out, questions
}
