package webrtclink

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"

	"github.com/arcships/rutis-network/link/tcp"
	rtc "github.com/pion/webrtc/v4"
)

const recordLimit = 16 << 10
const messageLimit = 16 << 20
const highWater = 256 << 10

var errProtocol = errors.New("incompatible-channel")
var errClosed = errors.New("channel-closed")

// The ordered channel allows one unacknowledged message in each direction.
// The receive callback never waits for a rutis RPC or a send acknowledgment.
type wire struct {
	channel    *rtc.DataChannel
	ctx        context.Context
	cancel     context.CancelCauseFunc
	mu         sync.Mutex
	partial    []byte
	delivering bool
	ack        chan struct{}
	messages   chan []byte
	drained    chan struct{}
	writer     sync.Mutex
}

func newWire(ctx context.Context, cancel context.CancelCauseFunc, dc *rtc.DataChannel) *wire {
	w := &wire{ctx: ctx, cancel: cancel, channel: dc, messages: make(chan []byte, 1), drained: make(chan struct{}, 1)}
	dc.SetBufferedAmountLowThreshold(highWater / 2)
	dc.OnBufferedAmountLow(func() {
		select {
		case w.drained <- struct{}{}:
		default:
		}
	})
	dc.OnClose(func() { cancel(errClosed) })
	dc.OnError(func(error) { cancel(errClosed) })
	dc.OnMessage(func(m rtc.DataChannelMessage) {
		if m.IsString || !w.take(m.Data) {
			cancel(errProtocol)
		}
	})
	return w
}

func (w *wire) take(record []byte) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(record) == 0 || len(record) > recordLimit || w.ctx.Err() != nil {
		return false
	}
	switch record[0] {
	case 3:
		if len(record) != 1 || w.ack == nil {
			return false
		}
		close(w.ack)
		w.ack = nil
		return true
	case 1:
		if len(record) < 5 || w.partial != nil || w.delivering {
			return false
		}
		size := binary.BigEndian.Uint32(record[1:5])
		if size > messageLimit || len(record)-5 > int(size) || size != 0 && len(record) == 5 {
			return false
		}
		w.partial = make([]byte, 0, int(size))
		w.partial = append(w.partial, record[5:]...)
	case 2:
		if w.partial == nil || len(record) == 1 || len(record)-1 > cap(w.partial)-len(w.partial) {
			return false
		}
		w.partial = append(w.partial, record[1:]...)
	default:
		return false
	}
	if len(w.partial) == cap(w.partial) {
		w.delivering = true
		w.messages <- w.partial
		w.partial = nil
	}
	return true
}

func (w *wire) send(data []byte, limit int) (err error) {
	if !w.writer.TryLock() {
		w.cancel(errProtocol)
		return errProtocol
	}
	defer w.writer.Unlock()
	defer func() {
		if err != nil {
			w.cancel(err)
		}
	}()
	if len(data) > messageLimit || limit < 6 {
		return errProtocol
	}
	ack := make(chan struct{})
	for offset, first := 0, true; first || offset < len(data); first = false {
		if err = w.ctx.Err(); err != nil {
			return err
		}
		for w.channel.BufferedAmount() > highWater {
			select {
			case <-w.ctx.Done():
				return context.Cause(w.ctx)
			case <-w.drained:
			}
		}
		header := 1
		if first {
			header = 5
		}
		n := min(len(data)-offset, limit-header)
		record := make([]byte, header+n)
		record[0] = 2
		if first {
			record[0] = 1
			binary.BigEndian.PutUint32(record[1:5], uint32(len(data)))
		}
		copy(record[header:], data[offset:offset+n])
		offset += n
		if offset == len(data) {
			w.mu.Lock()
			w.ack = ack
			w.mu.Unlock()
		}
		if err = w.channel.Send(record); err != nil {
			return err
		}
	}
	select {
	case <-w.ctx.Done():
		return context.Cause(w.ctx)
	case <-ack:
		return nil
	}
}

func (w *wire) serve(row context.Context, sessions tcp.Sessions, limit int, connected func()) (err error) {
	sessionClosed := errors.New("session-closed")
	near, err := sessions.Open(row, tcp.Far{
		Send:  func(b []byte) error { return w.send(b, limit) },
		Close: func(tcp.CloseReason) { w.cancel(sessionClosed) },
	})
	if err != nil {
		return err
	}
	defer func() {
		w.cancel(err)
		if context.Cause(w.ctx) != sessionClosed && row.Err() == nil {
			_ = near.End(row, tcp.CloseReason{Kind: "done"})
		}
	}()
	if w.ctx.Err() == nil {
		connected()
	}
	for {
		select {
		case <-w.ctx.Done():
			return context.Cause(w.ctx)
		case data := <-w.messages:
			if err := near.Receive(w.ctx, data); err != nil {
				return err
			}
			w.mu.Lock()
			w.delivering = false
			w.mu.Unlock()
			if err := w.channel.Send([]byte{3}); err != nil {
				return err
			}
		}
	}
}
