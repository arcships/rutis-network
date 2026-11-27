package grpclink

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/arcships/rutis-network/link/grpc/wire"
	"github.com/arcships/rutis-network/link/tcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func startTestServer(t *testing.T, identity Identity, register func(*grpc.Server)) (*grpc.Server, string) {
	t.Helper()
	cfg, err := identity.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(cfg)))
	register(server)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	return server, listener.Addr().String()
}

func bindTestEndpoint(t *testing.T, server *grpc.Server, near tcp.Near) *Endpoint {
	t.Helper()
	endpoint, err := Register(server, "device.test")
	if err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	endpoint.bound = &binding{row: t.Context(), run: run, cancel: cancel,
		sessions: tcp.Sessions{Open: func(context.Context, tcp.Far) (tcp.Near, error) { return near, nil }},
	}
	return endpoint
}

func waitClientStatus(t *testing.T, c *Client, want func(ClientStatus) bool) ClientStatus {
	t.Helper()
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		s := c.Status()
		if want(s) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("unexpected client state: %+v", c.Status())
	return ClientStatus{}
}

func TestEndpointDrainBeforeGracefulStop(t *testing.T) {
	serverID, device, _ := testIdentities(t)
	delivered := make(chan struct{})
	cleanupStarted, resume := make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(resume) })
	defer finish()
	var endpoint *Endpoint
	server, addr := startTestServer(t, serverID, func(server *grpc.Server) {
		healthpb.RegisterHealthServer(server, health.NewServer())
		endpoint = bindTestEndpoint(t, server, tcp.Near{
			Receive: func(context.Context, []byte) error {
				close(delivered)
				return nil
			},
			End: func(context.Context, tcp.CloseReason) error {
				close(cleanupStarted)
				<-resume
				return nil
			},
		})
	})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	conn := testConn(t, addr, device)
	client := wire.NewLinkClient(conn)
	stream, err := client.Tunnel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&wire.Frame{Payload: []byte("online")}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-delivered:
	case <-ctx.Done():
		t.Fatal("device session did not open")
	}
	until, stop := context.WithCancel(ctx)
	defer stop()
	first := make(chan error, 1)
	go func() { first <- endpoint.Drain(until) }()
	select {
	case <-cleanupStarted:
	case <-ctx.Done():
		t.Fatal("session cleanup did not start")
	}
	stop()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted Drain: %v", err)
	}
	replacement, err := client.Tunnel(ctx)
	if err == nil {
		_, err = replacement.Recv()
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("admission during drain: %v", err)
	}
	health, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil || health.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("application health: %v, %v", health, err)
	}
	finish()
	if err := endpoint.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := endpoint.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { server.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("GracefulStop blocked on an idle device")
	}
}

func TestReconnectReadsRotatedCredentials(t *testing.T) {
	serverID, device, _ := testIdentities(t)
	_, stale, _ := testIdentities(t)
	near := tcp.Near{End: func(context.Context, tcp.CloseReason) error { return nil }}
	_, addr := startTestServer(t, serverID, func(server *grpc.Server) { bindTestEndpoint(t, server, near) })
	client := &Client{row: t.Context(), config: ClientConfig{Identity: stale, Address: addr, ServerName: "cloud.test"},
		sessions: tcp.Sessions{Open: func(context.Context, tcp.Far) (tcp.Near, error) { return near, nil }}}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Stop)
	waitClientStatus(t, client, func(s ClientStatus) bool { return s.Phase == "retrying" })
	replace := func(dst, src string) {
		t.Helper()
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	replace(stale.CAFile, device.CAFile)
	replace(stale.CertFile, device.CertFile)
	// One retry sees the new certificate before its matching private key.
	waitClientStatus(t, client, func(s ClientStatus) bool {
		return s.Phase == "retrying" && s.LastError == codes.FailedPrecondition.String()
	})
	replace(stale.KeyFile, device.KeyFile)
	waitClientStatus(t, client, func(s ClientStatus) bool { return s.Phase == "admitted" })
}

type revokedTunnel struct {
	wire.UnimplementedLinkServer
	revoke chan struct{}
}

func (s *revokedTunnel) Tunnel(stream wire.Link_TunnelServer) error {
	if err := stream.SendHeader(metadata.Pairs(admittedHeader, "1")); err != nil {
		return err
	}
	if err := stream.Send(&wire.Frame{Payload: []byte("notice")}); err != nil {
		return err
	}
	select {
	case <-s.revoke:
		return status.Error(codes.PermissionDenied, "device access revoked")
	case <-stream.Context().Done():
		return stream.Context().Err()
	}
}

func TestClientSendEOFPreservesPermissionDenied(t *testing.T) {
	serverID, device, _ := testIdentities(t)
	service := &revokedTunnel{revoke: make(chan struct{})}
	_, addr := startTestServer(t, serverID, func(server *grpc.Server) { wire.RegisterLinkServer(server, service) })
	opened := make(chan tcp.Far, 1)
	receiving := make(chan struct{})
	client := &Client{row: t.Context(), config: ClientConfig{Identity: device, Address: addr, ServerName: "cloud.test"},
		sessions: tcp.Sessions{Open: func(_ context.Context, far tcp.Far) (tcp.Near, error) {
			opened <- far
			return tcp.Near{Receive: func(ctx context.Context, _ []byte) error {
				close(receiving)
				<-ctx.Done()
				return ctx.Err()
			}, End: func(context.Context, tcp.CloseReason) error { return nil }}, nil
		}}}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Stop)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	select {
	case <-receiving:
	case <-ctx.Done():
		t.Fatal("no in-flight receive")
	}
	far := <-opened
	start := time.Now()
	close(service.revoke)
	for {
		err := far.Send([]byte("in-flight"))
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("no send EOF")
		case <-time.After(5 * time.Millisecond):
		}
	}
	state := waitClientStatus(t, client, func(s ClientStatus) bool { return s.Phase == "retrying" })
	if state.LastError != codes.PermissionDenied.String() || state.RetryAt.Before(start.Add(30*time.Second)) {
		t.Fatalf("lost terminal status or denial backoff: %+v", state)
	}
}
