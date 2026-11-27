// Package wslink attaches an admitted WebSocket message channel to a rutis row.
package wslink

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/arcships/rutis-network/link/tcp"
	"github.com/gorilla/websocket"
)

const protocol = "rutis.5"
const maxMessage = 16 << 20
const pingInterval = 10 * time.Second

var errIncompatible = errors.New("incompatible websocket protocol")

func closeFrame(reason tcp.CloseReason) []byte {
	code := websocket.CloseNormalClosure
	switch reason.Kind {
	case "replaced":
		code = 4002
	case "rejected":
		code = 4003
	}
	// Control frames allow 123 bytes of UTF-8 after the status code.
	text := strings.ToValidUTF8(reason.Reason, "")
	if len(text) > 123 {
		text = text[:123]
		for len(text) > 0 && !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return websocket.FormatCloseMessage(code, text)
}

func serveSession(parent, row context.Context, conn *websocket.Conn, sessions tcp.Sessions, interval time.Duration) error {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	closeConn := sync.OnceFunc(func() { _ = conn.Close() })
	interrupt := context.AfterFunc(ctx, closeConn)
	defer func() {
		interrupt()
		closeConn()
	}()
	conn.SetReadLimit(maxMessage)
	var writer sync.Mutex
	var localClose atomic.Bool
	far := tcp.Far{
		Send: func(data []byte) error {
			writer.Lock()
			defer writer.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			var err error
			switch {
			case len(data) > maxMessage:
				err = errors.New("frame-too-large")
			case !utf8.Valid(data):
				err = errors.New("invalid session text")
			default:
				_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
				err = conn.WriteMessage(websocket.TextMessage, data)
			}
			if err != nil {
				cancel(err)
			}
			return err
		},
		Close: func(reason tcp.CloseReason) {
			if localClose.CompareAndSwap(false, true) {
				_ = conn.WriteControl(websocket.CloseMessage, closeFrame(reason), time.Now().Add(time.Second))
				cancel(nil)
			}
		},
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	near, err := sessions.Open(row, far)
	if err != nil {
		return err
	}
	// The reader owns both the control handlers and the read deadline.
	resetDeadline := func() error { return conn.SetReadDeadline(time.Now().Add(3 * interval)) }
	conn.SetPongHandler(func(string) error { return resetDeadline() })
	replyPing := conn.PingHandler()
	conn.SetPingHandler(func(data string) error {
		if err := resetDeadline(); err != nil {
			return err
		}
		return replyPing(data)
	})
	pingsDone := make(chan struct{})
	go func() {
		defer close(pingsDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(3*interval)); err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	reason := tcp.CloseReason{Kind: "done"}
	for ctx.Err() == nil {
		// Time spent waiting for near.Receive is local backpressure.
		if err = resetDeadline(); err != nil {
			break
		}
		var kind int
		var data []byte
		kind, data, err = conn.ReadMessage()
		if err != nil {
			var closed *websocket.CloseError
			if errors.As(err, &closed) {
				switch closed.Code {
				case 4002:
					reason.Kind = "replaced"
				case 4003:
					reason = tcp.CloseReason{Kind: "rejected", Reason: closed.Text}
				case websocket.CloseNormalClosure, websocket.CloseGoingAway:
					err = nil
				}
			}
			break
		}
		if kind != websocket.TextMessage || !utf8.Valid(data) {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseUnsupportedData, "expected UTF-8 text"), time.Now().Add(time.Second))
			err = errIncompatible
			break
		}
		if err = near.Receive(ctx, data); err != nil {
			break
		}
	}
	if cause := context.Cause(ctx); cause != nil {
		err = cause
	}
	cancel(nil)
	closeConn()
	<-pingsDone
	if localClose.Load() {
		return nil
	}
	if row.Err() == nil {
		if endErr := near.End(row, reason); err == nil {
			err = endErr
		}
	}
	return err
}
