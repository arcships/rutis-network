package libp2p

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"

	"github.com/libp2p/go-libp2p/core/network"
)

const maxFrame = 16 << 20

var errOversize = errors.New("frame-too-large")
var errTruncated = errors.New("truncated-frame")
var errSessionClosed = errors.New("session closed")

type closeReason struct {
	Kind string `json:"kind"`
}
type far struct {
	Send  func([]byte) error `json:"send"`
	Close func(closeReason)  `json:"close"`
}
type near struct {
	Receive func(context.Context, []byte) error
	End     func(context.Context, closeReason) error
}
type sessions struct {
	Open func(context.Context, far) (near, error)
}

func readFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, errTruncated
		}
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > maxFrame {
		return nil, errOversize
	}
	payload := make([]byte, size)
	_, err := io.ReadFull(r, payload)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, errTruncated
	}
	return payload, err
}

func writeFrame(w io.Writer, data []byte) error {
	if len(data) > maxFrame {
		return errOversize
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := w.Write(header[:]); err != nil || len(data) == 0 {
		return err
	}
	_, err := w.Write(data)
	return err
}

// The run context interrupts I/O on stop. The row context stays alive long
// enough to tell rutis that a manually stopped transport has ended.
func bridge(run, row context.Context, stream network.Stream, service sessions, opened func()) error {
	ctx, cancel := context.WithCancelCause(run)
	var once sync.Once
	closeStream := func(why error) {
		once.Do(func() {
			if errors.Is(why, io.EOF) || errors.Is(why, context.Canceled) || errors.Is(why, errSessionClosed) {
				_ = stream.Close()
			} else {
				_ = stream.Reset()
			}
		})
	}
	interrupt := context.AfterFunc(ctx, func() { closeStream(context.Cause(ctx)) })
	defer func() {
		interrupt()
		cancel(nil)
	}()

	var writer sync.Mutex
	local, err := service.Open(ctx, far{
		Send: func(data []byte) error {
			writer.Lock()
			defer writer.Unlock()
			err := writeFrame(stream, data)
			if err != nil {
				cancel(err)
			}
			return err
		},
		Close: func(closeReason) { cancel(errSessionClosed) },
	})
	if err != nil {
		closeStream(err)
		return err
	}
	opened()
	for {
		var payload []byte
		payload, err = readFrame(stream)
		if err != nil {
			break
		}
		if err = local.Receive(ctx, payload); err != nil {
			break
		}
	}
	if cause := context.Cause(ctx); cause != nil {
		err = cause
	}
	closeStream(err)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if errors.Is(err, errSessionClosed) {
		return nil
	}
	if row.Err() == nil {
		endErr := local.End(row, closeReason{Kind: "done"})
		if err == nil {
			err = endErr
		}
	}
	return err
}
