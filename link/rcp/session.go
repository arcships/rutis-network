package rcp

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
)

// serveSession owns the single reader. All application and heartbeat writes share one
// writer. Waiting for near.Receive applies local backpressure before the next read.
func serveSession(parent, row context.Context, ch *channel, sessions tcp.Sessions) error {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	stopClose := context.AfterFunc(ctx, func() { ch.conn.Close() })
	defer stopClose()
	var writer sync.Mutex
	var localClose atomic.Bool
	send := func(kind byte, b []byte) error {
		writer.Lock()
		defer writer.Unlock()
		if localClose.Load() {
			return done
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		ch.conn.SetWriteDeadline(time.Now().Add(3 * ch.beat))
		if err := ch.write(kind, b); err != nil {
			cancel(err)
			return err
		}
		return nil
	}
	far := tcp.Far{Send: func(b []byte) error { return send(dataMessage, b) }, Close: func(r tcp.CloseReason) {
		if ctx.Err() != nil || !localClose.CompareAndSwap(false, true) {
			return
		}
		kind := closeDone
		switch r.Kind {
		case "replaced":
			kind = closeReplaced
		case "rejected":
			kind = closeRejected
		}
		// The shutdown decision is final even if the notification fails. Never
		// queue a control notification behind a stalled application write.
		if writer.TryLock() {
			ch.conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = ch.write(kind, nil)
			writer.Unlock()
		}
		cancel(done)
	}}
	near, err := sessions.Open(row, far)
	if err != nil {
		return refused
	}
	heartDone := make(chan struct{})
	go func() {
		defer close(heartDone)
		ticker := time.NewTicker(ch.beat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if send(ping, nil) != nil {
					return
				}
			}
		}
	}()
	reason := tcp.CloseReason{Kind: "done"}
	for ctx.Err() == nil {
		ch.conn.SetReadDeadline(time.Now().Add(3 * ch.beat))
		kind, b, readErr := ch.read()
		if readErr != nil {
			err = readErr
			break
		}
		switch kind {
		case dataMessage:
			if near.Receive(ctx, b) != nil {
				err = refused
			}
		case ping:
			err = send(pong, nil)
		case pong:
		case closeDone:
			err = peerClosed
		case closeReplaced:
			reason.Kind = "replaced"
			err = replaced
		case closeRejected:
			reason.Kind = "rejected"
			err = refused
		default:
			err = badFrame
		}
		if err != nil {
			break
		}
	}
	if cause := context.Cause(ctx); cause != nil {
		err = cause
	}
	cancel(err)
	<-heartDone
	if localClose.Load() {
		return done
	}
	if row.Err() == nil {
		if endErr := near.End(row, reason); endErr != nil && err == nil {
			err = refused
		}
	}
	return err
}
