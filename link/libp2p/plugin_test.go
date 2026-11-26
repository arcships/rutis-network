package libp2p

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	p2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	circuit "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
)

func TestFrameLimits(t *testing.T) {
	for _, size := range []int{0, 3, maxFrame} {
		data := bytes.Repeat([]byte{0xfe}, size)
		var b bytes.Buffer
		if err := writeFrame(&b, data); err != nil {
			t.Fatal(err)
		}
		if n := binary.BigEndian.Uint32(b.Bytes()); n != uint32(size) {
			t.Fatalf("frame length %d", n)
		}
		got, err := readFrame(&b)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("round trip size %d: %v", size, err)
		}
	}
	for _, tc := range []struct {
		input []byte
		err   error
	}{
		{nil, io.EOF}, {[]byte{0, 0}, errTruncated}, {[]byte{0, 0, 0, 2, 1}, errTruncated}, {[]byte{1, 0, 0, 1}, errOversize},
	} {
		if _, err := readFrame(bytes.NewReader(tc.input)); !errors.Is(err, tc.err) {
			t.Fatalf("want %v, got %v", tc.err, err)
		}
	}
	if err := writeFrame(io.Discard, make([]byte, maxFrame+1)); !errors.Is(err, errOversize) {
		t.Fatal(err)
	}
}

func identity(t *testing.T) (string, peer.ID) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "peer.key")
	id, err := GenerateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = GenerateKey(path); err == nil {
		t.Fatal("keygen overwrote an existing identity")
	}
	return path, id
}

func awaitPhase(t *testing.T, c *control, phase string) snapshot {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		s := c.Status()
		if s.Phase == phase {
			return s
		}
		if s.Phase == "failed" {
			t.Fatalf("%s before %s", s.LastError, phase)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waiting for %s: %+v", phase, c.Status())
	return snapshot{}
}

func start(t *testing.T, cfg config, svc sessions) *control {
	t.Helper()
	opts, err := cfg.parse()
	if err != nil {
		t.Fatal(err)
	}
	c := &control{row: t.Context(), opts: opts, service: svc}
	if err = c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Stop() })
	return c
}

func echoService() sessions {
	return sessions{Open: func(_ context.Context, f far) (near, error) {
		return near{
			Receive: func(_ context.Context, msg []byte) error { return f.Send(msg) },
			End:     func(context.Context, closeReason) error { return nil },
		}, nil
	}}
}

func TestDirectAndRelay(t *testing.T) {
	for _, mode := range []string{"direct", "relay"} {
		t.Run(mode, func(t *testing.T) {
			serverKey, serverID := identity(t)
			clientKey, clientID := identity(t)
			serverCfg := config{KeyFile: serverKey, Listen: "/ip4/127.0.0.1/udp/0/quic-v1", AllowPeer: clientID.String()}
			if mode == "relay" {
				h, err := p2p.New(p2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { h.Close() })
				reservations := make(chan struct{}, 1)
				relay, err := circuit.New(h, circuit.WithACL(reservationWitness{reservations}))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { relay.Close() })
				serverCfg.Relay = h.Addrs()[0].String() + "/p2p/" + h.ID().String()
				// Dial after the relay has received the listener's reservation.
				listener := start(t, serverCfg, echoService())
				awaitPhase(t, listener, "listening")
				select {
				case <-reservations:
				case <-time.After(10 * time.Second):
					t.Fatal("no relay reservation")
				}
				roundTrip(t, listener, config{KeyFile: clientKey, Dial: serverCfg.Relay + "/p2p-circuit/p2p/" + serverID.String()}, mode)
			} else {
				listener := start(t, serverCfg, echoService())
				s := awaitPhase(t, listener, "listening")
				roundTrip(t, listener, config{KeyFile: clientKey, Dial: s.ListenAddrs[0] + "/p2p/" + serverID.String()}, mode)
			}
		})
	}
}

func roundTrip(t *testing.T, listener *control, cfg config, path string) {
	t.Helper()
	opened := make(chan far, 1)
	received := make(chan []byte, 1)
	svc := sessions{Open: func(_ context.Context, f far) (near, error) {
		opened <- f
		return near{
			Receive: func(_ context.Context, b []byte) error {
				received <- b
				return nil
			},
			End: func(context.Context, closeReason) error { return nil },
		}, nil
	}}
	dialer := start(t, cfg, svc)
	for attempt := 0; attempt < 2; attempt++ {
		s := awaitPhase(t, dialer, "connected")
		if s.Path != path {
			t.Fatalf("want %s, got %+v", path, s)
		}
		var remote far
		select {
		case remote = <-opened:
		case <-time.After(time.Second):
			t.Fatal("session never opened")
		}
		for _, payload := range [][]byte{nil, []byte("rutis"), {0, 255, 10}} {
			if err := remote.Send(payload); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-received:
				if !bytes.Equal(got, payload) {
					t.Fatalf("got %x", got)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no echo")
			}
		}
		if s := awaitPhase(t, listener, "connected"); s.Path != path {
			t.Fatalf("listener: %+v", s)
		}
		if err := dialer.Start(); err != nil {
			t.Fatal(err)
		}
		if err := dialer.Stop(); err != nil {
			t.Fatal(err)
		}
		awaitPhase(t, listener, "listening")
		if attempt == 0 {
			if err := dialer.Start(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestRejectsUnlistedPeerAndExtraStream(t *testing.T) {
	key, serverID := identity(t)
	h, err := p2p.New(p2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	cfg := config{KeyFile: key, Listen: "/ip4/127.0.0.1/udp/0/quic-v1", AllowPeer: h.ID().String()}
	opened := make(chan struct{}, 3)
	svc := echoService()
	open := svc.Open
	svc.Open = func(ctx context.Context, f far) (near, error) {
		opened <- struct{}{}
		return open(ctx, f)
	}
	listener := start(t, cfg, svc)
	addr := awaitPhase(t, listener, "listening").ListenAddrs[0] + "/p2p/" + serverID.String()
	info, err := addressedPeer(addr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	attempt := func(s network.Stream, accepted bool) {
		t.Helper()
		s.SetDeadline(time.Now().Add(3 * time.Second))
		if err := writeFrame(s, []byte("alive")); err != nil && accepted {
			t.Fatal(err)
		}
		data, err := readFrame(s)
		if accepted && (err != nil || string(data) != "alive") {
			t.Fatalf("accepted stream: %q %v", data, err)
		}
		if !accepted && !errors.Is(err, network.ErrReset) {
			t.Fatalf("expected a refused stream to be reset, got %v", err)
		}
	}
	stranger, err := p2p.New(p2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer stranger.Close()
	if err = stranger.Connect(ctx, *info); err != nil {
		t.Fatal(err)
	}
	denied, err := stranger.NewStream(ctx, serverID, protocolID)
	if err != nil {
		t.Fatal(err)
	}
	attempt(denied, false)
	if len(opened) != 0 {
		t.Fatal("opened session for unlisted peer")
	}
	if err = h.Connect(ctx, *info); err != nil {
		t.Fatal(err)
	}
	first, err := h.NewStream(ctx, serverID, protocolID)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	attempt(first, true)
	second, err := h.NewStream(ctx, serverID, protocolID)
	if err != nil {
		t.Fatal(err)
	}
	attempt(second, false)
	attempt(first, true)
	if len(opened) != 1 {
		t.Fatalf("%d sessions mounted", len(opened))
	}
}

type reservationWitness struct{ granted chan struct{} }

func (r reservationWitness) AllowReserve(peer.ID, ma.Multiaddr) bool {
	select {
	case r.granted <- struct{}{}:
	default:
	}
	return true
}
func (reservationWitness) AllowConnect(peer.ID, ma.Multiaddr, peer.ID) bool { return true }
