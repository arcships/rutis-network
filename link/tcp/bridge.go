// Package tcp frames an admitted byte stream for a rutis session.
package tcp

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
)

const maxFrame = 16 << 20

var (
	errTooLarge   = errors.New("frame-too-large")
	errTruncated  = errors.New("truncated-frame")
	errConcurrent = errors.New("concurrent-send")
	errFarClosed  = errors.New("session closed")
)

// Sessions binds the injected rutis/sessions service using the Go SDK.
type Sessions struct {
	Open func(context.Context, Far) (Near, error)
}

// Far gives the session access to the admitted connection.
type Far struct {
	Send  func([]byte) error `json:"send"`
	Close func(CloseReason)  `json:"close"`
}

// CloseReason is the rutis channel close record. TCP closes both directions.
type CloseReason struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
}

// Near receives complete frames and the end of the connection.
type Near struct {
	Receive func(context.Context, []byte) error
	End     func(context.Context, CloseReason) error
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
		return nil, errTooLarge
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, errTruncated
		}
		return nil, err
	}
	return body, nil
}

func writeFrame(conn net.Conn, body []byte) error {
	if len(body) > maxFrame {
		return errTooLarge
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	if _, err := conn.Write(header[:]); err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	_, err := conn.Write(body)
	return err
}

func receiveFrames(ctx context.Context, conn net.Conn, near Near) error {
	for ctx.Err() == nil {
		body, err := readFrame(conn)
		if err != nil {
			return err
		}
		if err := near.Receive(ctx, body); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// Open takes ownership of conn after admission. Pass the row's lifetime context.
// The returned channel reports the result after the connection and session end.
func Open(parent context.Context, conn net.Conn, sessions Sessions) (<-chan error, error) {
	return openStream(parent, parent, conn, sessions)
}

// Stopping a managed transport leaves its row alive. End the old session using
// that row's context even after cancellation has interrupted the transport.
func openStream(parent, row context.Context, conn net.Conn, sessions Sessions) (<-chan error, error) {
	ctx, cancel := context.WithCancelCause(parent)
	closeConn := sync.OnceFunc(func() { conn.Close() })
	stopClose := context.AfterFunc(ctx, closeConn)
	finish := func() {
		stopClose()
		closeConn()
	}
	var writer sync.Mutex
	send := func(body []byte) error {
		var err error
		if !writer.TryLock() {
			err = errConcurrent
		} else {
			defer writer.Unlock()
			err = writeFrame(conn, body)
		}
		if err != nil {
			cancel(err)
			closeConn()
		}
		return err
	}
	far := Far{
		Send: send,
		Close: func(CloseReason) {
			cancel(errFarClosed)
			closeConn()
		},
	}
	near, err := sessions.Open(ctx, far)
	if err != nil {
		cancel(err)
		finish()
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		// A failed send closes the socket too. Report its original error.
		cancel(receiveFrames(ctx, conn, near))
		err := context.Cause(ctx)
		finish()
		if errors.Is(err, errFarClosed) {
			err = nil
		} else {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			if row.Err() == nil {
				endErr := near.End(row, CloseReason{Kind: "done"})
				if err == nil {
					err = endErr
				}
			}
		}
		done <- err
		close(done)
	}()
	return done, nil
}
