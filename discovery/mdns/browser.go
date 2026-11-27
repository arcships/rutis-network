package mdns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type socket struct {
	conn  *net.UDPConn
	group *net.UDPAddr
	read  func([]byte) (int, int, net.Addr, error)
}

func listen(iface *net.Interface, network, group string) (*socket, error) {
	addr := &net.UDPAddr{IP: net.ParseIP(group), Port: 5353}
	if network == "udp6" {
		addr.Zone = iface.Name
	}
	conn, err := net.ListenMulticastUDP(network, iface, addr)
	if err != nil {
		return nil, err
	}
	s := &socket{conn: conn, group: addr}
	// ListenMulticastUDP disables local loopback. A second row or a native
	// DNS-SD daemon on this machine must be able to see our questions.
	if network == "udp4" {
		pc := ipv4.NewPacketConn(conn)
		err = pc.SetControlMessage(ipv4.FlagInterface|ipv4.FlagDst, true)
		if err == nil {
			err = pc.SetMulticastLoopback(true)
		}
		if err == nil {
			err = pc.SetMulticastTTL(255)
		}
		s.read = func(buf []byte) (int, int, net.Addr, error) {
			n, cm, from, err := pc.ReadFrom(buf)
			if cm == nil || !cm.Dst.Equal(addr.IP) {
				return n, 0, from, err
			}
			return n, cm.IfIndex, from, err
		}
	} else {
		pc := ipv6.NewPacketConn(conn)
		err = pc.SetControlMessage(ipv6.FlagInterface|ipv6.FlagDst, true)
		if err == nil {
			err = pc.SetMulticastLoopback(true)
		}
		if err == nil {
			err = pc.SetMulticastHopLimit(255)
		}
		s.read = func(buf []byte) (int, int, net.Addr, error) {
			n, cm, from, err := pc.ReadFrom(buf)
			if cm == nil || !cm.Dst.Equal(addr.IP) {
				return n, 0, from, err
			}
			return n, cm.IfIndex, from, err
		}
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

type browser struct {
	index   int
	sockets []*socket
}

func openBrowser(iface *net.Interface) (*browser, error) {
	b := &browser{index: iface.Index}
	var errs []error
	for _, g := range []struct{ network, addr string }{{"udp4", "224.0.0.251"}, {"udp6", "ff02::fb"}} {
		s, err := listen(iface, g.network, g.addr)
		if err != nil {
			errs = append(errs, err)
		} else {
			b.sockets = append(b.sockets, s)
		}
	}
	if len(b.sockets) == 0 {
		return nil, fmt.Errorf("join mDNS on %s: %w", iface.Name, errors.Join(errs...))
	}
	return b, nil
}

type packet struct {
	msg *dns.Msg
	err error
}

func (b *browser) receive(ctx context.Context, s *socket, ch chan<- packet) {
	buf := make([]byte, 65535)
	for {
		n, index, from, err := s.read(buf)
		var msg dns.Msg
		if err == nil {
			addr, ok := from.(*net.UDPAddr)
			if !ok || addr.Port != 5353 || index != b.index || msg.Unpack(buf[:n]) != nil || !msg.Response {
				continue
			}
		}
		select {
		case ch <- packet{&msg, err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (b *browser) send(msg *dns.Msg) error {
	data, err := msg.Pack()
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range b.sockets {
		if _, err := s.conn.WriteToUDP(data, s.group); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == len(b.sockets) {
		return errors.Join(errs...)
	}
	return nil
}

type attempt struct {
	at    time.Time
	delay time.Duration
}

func (b *browser) run(parent context.Context, c *cache, svc *observations) error {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	ch := make(chan packet, 16)
	for _, s := range b.sockets {
		wg.Add(1)
		go func() { defer wg.Done(); b.receive(ctx, s, ch) }()
	}
	defer func() {
		cancel()
		for _, s := range b.sockets {
			s.conn.Close()
		}
		wg.Wait()
	}()
	// Wake even on a silent LAN so stale observations disappear.
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	tried := map[dns.Question]attempt{}
	for {
		now := time.Now()
		list, questions := c.view(now)
		svc.update(list)
		for q := range tried {
			if _, ok := questions[q]; !ok {
				delete(tried, q)
			}
		}
		var batch []dns.Question
		for q, refresh := range questions {
			prev := tried[q]
			delay := prev.delay
			if refresh {
				delay = time.Second
			}
			if now.Sub(prev.at) < delay {
				continue
			}
			tried[q] = attempt{now, min(time.Hour, max(time.Second, prev.delay*2))}
			batch = append(batch, q)
			// Four uncompressed maximum-length names fit in one LAN datagram.
			if len(batch) == 4 {
				if err := b.send(c.query(batch, now)); err != nil {
					return err
				}
				batch = nil
			}
		}
		if len(batch) > 0 {
			if err := b.send(c.query(batch, now)); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case p := <-ch:
			if p.err != nil {
				return p.err
			}
			c.ingest(p.msg, time.Now())
		case <-tick.C:
		}
	}
}
