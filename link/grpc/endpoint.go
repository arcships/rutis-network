package grpclink

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/arcships/rutis-network/link/grpc/wire"
	"github.com/arcships/rutis-network/link/tcp"
	"github.com/arcships/rutis/go/rutis"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Endpoint belongs to the application's gRPC server. Its rutis row can be
// attached and unloaded independently of the server and its other RPCs.
type Endpoint struct {
	wire.UnimplementedLinkServer
	clientName string
	mu         sync.Mutex
	bound      *binding
}

type binding struct {
	row      context.Context
	run      context.Context
	sessions tcp.Sessions
	active   chan struct{}
}

// Register adds the versioned Tunnel method to an existing server before Serve.
// The application configures mTLS and the gRPC message limits on that server.
func Register(server grpc.ServiceRegistrar, clientName string) (*Endpoint, error) {
	if clientName == "" {
		return nil, errors.New("client certificate DNS name is required")
	}
	e := &Endpoint{clientName: clientName}
	wire.RegisterLinkServer(server, e)
	return e, nil
}

// Bind connects the registered method to this plugin's injected session service.
func (e *Endpoint) Bind(row *rutis.Ctx) error {
	var sessions tcp.Sessions
	if err := row.Use("rutis/sessions", &sessions); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(row)
	b := &binding{row: row, run: ctx, sessions: sessions}
	e.mu.Lock()
	if e.bound != nil {
		e.mu.Unlock()
		cancel()
		return errors.New("gRPC endpoint already bound")
	}
	e.bound = b
	e.mu.Unlock()
	row.Effect(func(context.Context) error {
		e.mu.Lock()
		cancel()
		active := b.active
		e.mu.Unlock()
		if active != nil {
			<-active
		}
		e.mu.Lock()
		e.bound = nil
		e.mu.Unlock()
		return nil
	})
	return nil
}

func (e *Endpoint) Tunnel(stream grpc.BidiStreamingServer[wire.Frame, wire.Frame]) error {
	p, ok := peer.FromContext(stream.Context())
	if !ok {
		return status.Error(codes.Unauthenticated, "mTLS required")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.PeerCertificates) == 0 {
		return status.Error(codes.Unauthenticated, "verified client certificate required")
	}
	if tlsInfo.State.PeerCertificates[0].VerifyHostname(e.clientName) != nil {
		return status.Error(codes.PermissionDenied, "client certificate name rejected")
	}
	e.mu.Lock()
	b := e.bound
	if b == nil || b.run.Err() != nil {
		e.mu.Unlock()
		return status.Error(codes.Unavailable, "plugin offline")
	}
	if b.active != nil {
		e.mu.Unlock()
		return status.Error(codes.ResourceExhausted, "plugin already connected")
	}
	b.active = make(chan struct{})
	e.mu.Unlock()
	release := func() {
		e.mu.Lock()
		close(b.active)
		b.active = nil
		e.mu.Unlock()
	}

	if err := stream.SendHeader(metadata.Pairs(admittedHeader, "1")); err != nil {
		release()
		return err
	}
	ctx, interrupt := context.WithCancelCause(stream.Context())
	stop := context.AfterFunc(b.run, func() { interrupt(context.Canceled) })
	defer func() { stop(); interrupt(context.Canceled) }()
	// Returning from the RPC interrupts flow-controlled server I/O. Let the
	// session finish its End callback before admitting a replacement stream.
	done := make(chan error, 1)
	go func() {
		defer release()
		done <- serveSession(ctx, b.row, stream, b.sessions, interrupt)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		err := context.Cause(ctx)
		if errors.Is(err, io.EOF) || errors.Is(err, errSessionClosed) {
			return nil
		}
		return err
	}
}
