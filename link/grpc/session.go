// Package grpclink connects a rutis plugin to a plugin embedded in a gRPC service.
package grpclink

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/arcships/rutis-network/link/grpc/wire"
	"github.com/arcships/rutis-network/link/tcp"
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

// Each gRPC message already contains one complete rutis message. A server-side
// Recv may stay blocked until the RPC handler returns, so receive in a worker.
func serveSession(run, row context.Context, stream messageStream, sessions tcp.Sessions, interrupt func(error)) error {
	ctx, cancel := context.WithCancelCause(run)
	defer cancel(nil)
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
			if err != nil {
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
	go func() {
		for ctx.Err() == nil {
			frame, err := stream.Recv()
			if err != nil {
				cancel(err)
				return
			}
			if len(frame.Payload) > MaxPayload {
				cancel(status.Error(codes.ResourceExhausted, "frame-too-large"))
				return
			}
			if err := ctx.Err(); err != nil {
				return
			}
			if err := near.Receive(ctx, frame.Payload); err != nil {
				cancel(err)
				return
			}
		}
	}()
	<-ctx.Done()
	cause := context.Cause(ctx)
	interrupt(cause)
	if errors.Is(cause, errSessionClosed) {
		return nil
	}
	if row.Err() == nil {
		if err := near.End(row, tcp.CloseReason{Kind: "done"}); err != nil && errors.Is(cause, io.EOF) {
			return err
		}
	}
	if errors.Is(cause, io.EOF) {
		return nil
	}
	return cause
}
