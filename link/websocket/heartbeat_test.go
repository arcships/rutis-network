package wslink

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"github.com/gorilla/websocket"
)

func TestIdlePeerAndSilentPeerRelease(t *testing.T) {
	ended := make(chan tcp.CloseReason, 2)
	received := make(chan string, 1)
	address, trusted := testListener(t, 150*time.Millisecond, tcp.Sessions{Open: func(context.Context, tcp.Far) (tcp.Near, error) {
		return tcp.Near{
			Receive: func(_ context.Context, data []byte) error { received <- string(data); return nil },
			End:     func(_ context.Context, reason tcp.CloseReason) error { ended <- reason; return nil },
		}, nil
	}})
	conn, _, err := dial(t.Context(), address, "pair", trusted)
	if err != nil {
		t.Fatal(err)
	}
	silent := make(chan struct{})
	reply := conn.PingHandler()
	pings := 0
	conn.SetPingHandler(func(data string) error {
		pings++
		if pings <= 4 {
			return reply(data)
		}
		if pings == 5 {
			close(silent)
		}
		return nil
	})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		_, _, _ = conn.ReadMessage() // Process control frames until the listener closes.
	}()
	defer func() { _ = conn.Close(); <-readerDone }()

	select {
	case <-silent:
	case reason := <-ended:
		t.Fatalf("idle peer responding to pings was disconnected: %v", reason)
	case <-time.After(5 * time.Second):
		t.Fatal("idle peer did not receive the expected pings")
	}
	select {
	case reason := <-ended:
		if reason.Kind != "done" {
			t.Fatalf("unexpected session end: %v", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener kept the silent session open")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	d := websocket.Dialer{TLSClientConfig: trusted, Subprotocols: []string{protocol}}
	for {
		next, resp, err := d.DialContext(ctx, address, http.Header{"Authorization": {"Bearer pair"}})
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err == nil {
			defer next.Close()
			if err := next.WriteMessage(websocket.TextMessage, []byte("reconnected")); err != nil {
				t.Fatal(err)
			}
			break
		}
		// End is delivered just before the listener releases the slot.
		if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("could not reconnect: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case got := <-received:
		if got != "reconnected" {
			t.Fatalf("unexpected message on replacement session: %q", got)
		}
	case <-ctx.Done():
		t.Fatal("replacement connection did not deliver a session message")
	}
}
