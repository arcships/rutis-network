// Package grpclink connects a rutis plugin to a plugin embedded in a gRPC service.
package grpclink

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/arcships/rutis-network/link/grpc/wire"
	"github.com/arcships/rutis-network/link/tcp"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const MaxPayload = 16 << 20

// MaxWireSize includes the protobuf field tag and varint length for a full frame.
const MaxWireSize = MaxPayload + 5

const admittedHeader = "rutis-link-accepted"

type messageStream interface {
	Send(*wire.Frame) error
	Recv() (*wire.Frame, error)
}

var errSessionClosed = errors.New("session closed")
var errAwaitTrailers = errors.New("client send ended before final gRPC status")

func receiveSession(ctx context.Context, stream messageStream, near tcp.Near) error {
	for ctx.Err() == nil || context.Cause(ctx) == errAwaitTrailers {
		frame, err := stream.Recv()
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			// Delivery has stopped. Only this reader may drain to the trailers.
			continue
		}
		if len(frame.Payload) > MaxPayload {
			return status.Error(codes.ResourceExhausted, "frame-too-large")
		}
		if err := near.Receive(ctx, frame.Payload); err != nil && context.Cause(ctx) != errAwaitTrailers {
			return err
		}
	}
	return context.Cause(ctx)
}

// Each gRPC message already contains one complete rutis message. A server-side
// Recv may stay blocked until the RPC handler returns, so receive in a worker.
func serveSession(run, row context.Context, stream messageStream, sessions tcp.Sessions, interrupt func(error)) error {
	ctx, cancel := context.WithCancelCause(run)
	defer cancel(nil)
	_, clientStream := stream.(grpc.ClientStream)
	var writer sync.Mutex
	far := tcp.Far{
		Send: func(data []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(data) > MaxPayload {
				err := status.Error(codes.ResourceExhausted, "frame-too-large")
				cancel(err)
				return err
			}
			if !writer.TryLock() {
				err := errors.New("concurrent-send")
				cancel(err)
				return err
			}
			defer writer.Unlock()
			err := stream.Send(&wire.Frame{Payload: data})
			if clientStream && errors.Is(err, io.EOF) {
				// grpc-go reports remote failures from Recv. Wake a blocked
				// near.Receive without canceling the underlying client RPC.
				cancel(errAwaitTrailers)
			} else if err != nil {
				cancel(err)
			}
			return err
		},
		Close: func(tcp.CloseReason) { cancel(errSessionClosed) },
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	near, err := sessions.Open(row, far)
	if err != nil {
		return err
	}
	received := make(chan error, 1)
	go func() {
		err := receiveSession(ctx, stream, near)
		received <- err
		cancel(err)
	}()
	<-ctx.Done()
	cause := context.Cause(ctx)
	if cause == errAwaitTrailers {
		select {
		case cause = <-received:
		case <-run.Done():
			cause = context.Cause(run)
		}
	}
	interrupt(cause)
	if errors.Is(cause, errSessionClosed) {
		return nil
	}
	if row.Err() == nil {
		if err := near.End(row, closeReason(cause)); err != nil && errors.Is(cause, io.EOF) {
			return err
		}
	}
	if errors.Is(cause, io.EOF) {
		return nil
	}
	return cause
}

func closeReason(err error) tcp.CloseReason {
	s := status.Convert(err)
	if s.Code() == codes.Aborted {
		for _, d := range s.Details() {
			if info, ok := d.(*errdetails.ErrorInfo); ok && info.Domain == "rutis.link.v1" && (info.Reason == "REPLACED" || info.Reason == "PEER_REPLACED") {
				return tcp.CloseReason{Kind: "replaced"}
			}
		}
	}
	if s.Code() == codes.Unauthenticated || s.Code() == codes.PermissionDenied {
		return tcp.CloseReason{Kind: "rejected", Reason: s.Code().String()}
	}
	return tcp.CloseReason{Kind: "done"}
}
