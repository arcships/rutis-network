package grpclink

import (
	"bytes"
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcships/rutis-network/link/grpc/wire"
	"github.com/arcships/rutis-network/link/tcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// Exercise the actual gRPC codec, flow control and handler lifetime with a
// local session service in place of the rutis host.
func TestTunnelFramesAndAdmission(t *testing.T) {
	serverID, device, other := testIdentities(t)
	cfg, err := serverID.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(cfg)), grpc.MaxRecvMsgSize(MaxWireSize), grpc.MaxSendMsgSize(MaxWireSize))
	e, err := Register(srv, "device.test")
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	fars := make(chan tcp.Far, 1)
	ended := make(chan struct{}, 1)
	run, cancelBinding := context.WithCancel(t.Context())
	defer cancelBinding()
	e.bound = &binding{row: t.Context(), run: run, cancel: cancelBinding, sessions: tcp.Sessions{Open: func(_ context.Context, f tcp.Far) (tcp.Near, error) {
		opens.Add(1)
		fars <- f
		return tcp.Near{Receive: func(_ context.Context, b []byte) error { return f.Send(b) }, End: func(context.Context, tcp.CloseReason) error { ended <- struct{}{}; return nil }}, nil
	}}}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	t.Cleanup(srv.Stop)
	addr := l.Addr().String()
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	attempt := func(id Identity) wire.Link_TunnelClient {
		t.Helper()
		stream, err := wire.NewLinkClient(testConn(t, addr, id)).Tunnel(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return stream
	}
	if _, err := attempt(other).Recv(); status.Code(err) != codes.PermissionDenied || opens.Load() != 0 {
		t.Fatalf("rejected peer opened %d sessions: %v", opens.Load(), err)
	}
	stream := attempt(device)
	h, err := stream.Header()
	if err != nil || len(h.Get(admittedHeader)) != 1 {
		t.Fatal(h, err)
	}
	far := <-fars
	if _, err := attempt(device).Recv(); status.Code(err) != codes.ResourceExhausted || opens.Load() != 1 {
		t.Fatalf("duplicate stream: %v", err)
	}
	for _, body := range [][]byte{nil, {0, 255, 10}, bytes.Repeat([]byte{123}, MaxPayload)} {
		if err := stream.Send(&wire.Frame{Payload: body}); err != nil {
			t.Fatal(err)
		}
		f, err := stream.Recv()
		if err != nil || !bytes.Equal(f.GetPayload(), body) {
			t.Fatalf("echo of %d bytes: %v", len(body), err)
		}
	}
	if err := far.Send(make([]byte, MaxPayload+1)); status.Code(err) != codes.ResourceExhausted {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversize stream outcome: %v", err)
	}
	select {
	case <-ended:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestClientRejectsStreamBeforeSession(t *testing.T) {
	serverID, _, other := testIdentities(t)
	cfg, err := serverID.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(cfg)))
	if _, err := Register(srv, "device.test"); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	t.Cleanup(srv.Stop)
	c := Client{row: t.Context(), sessions: tcp.Sessions{Open: func(context.Context, tcp.Far) (tcp.Near, error) {
		t.Error("opened client session before admission")
		return tcp.Near{}, context.Canceled
	}}}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := c.connect(ctx, testConn(t, l.Addr().String(), other)); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("admission error: %v", err)
	}
}

func TestUnbindInterruptsBlockedServerSend(t *testing.T) {
	serverID, device, _ := testIdentities(t)
	cfg, err := serverID.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(cfg)), grpc.MaxSendMsgSize(MaxWireSize))
	e, err := Register(srv, "device.test")
	if err != nil {
		t.Fatal(err)
	}
	run, stop := context.WithCancel(t.Context())
	defer stop()
	started, sendDone, ended := make(chan struct{}), make(chan struct{}), make(chan struct{})
	e.bound = &binding{row: t.Context(), run: run, cancel: stop, sessions: tcp.Sessions{Open: func(_ context.Context, f tcp.Far) (tcp.Near, error) {
		go func() {
			defer close(sendDone)
			frame := make([]byte, MaxPayload)
			close(started)
			for f.Send(frame) == nil {
			}
		}()
		return tcp.Near{Receive: func(context.Context, []byte) error { return nil }, End: func(context.Context, tcp.CloseReason) error {
			// Session cleanup can wait for the writer. The server RPC must
			// return so that a flow-controlled Send can finish.
			<-sendDone
			close(ended)
			return nil
		}}, nil
	}}}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	t.Cleanup(srv.Stop)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	stream, err := wire.NewLinkClient(testConn(t, l.Addr().String(), device)).Tunnel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Header(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stop()
	select {
	case <-ended:
	case <-ctx.Done():
		t.Fatal("unbind waited for a device that was not reading")
	}
}
