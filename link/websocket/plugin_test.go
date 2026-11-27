package wslink

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"github.com/gorilla/websocket"
)

func testListener(t *testing.T, interval time.Duration, sessions tcp.Sessions) (string, *tls.Config) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	l := &listener{path: "/rutis", tokenSum: sha256.Sum256([]byte("pair")), sessions: sessions, ctx: ctx, row: ctx, pingInterval: interval}
	server := httptest.NewTLSServer(l)
	t.Cleanup(func() {
		cancel()
		server.Close()
		l.active.Lock()
		l.active.Unlock()
	})
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	return "wss" + strings.TrimPrefix(server.URL, "https") + "/rutis", &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
}

func TestAdmissionPrecedesSession(t *testing.T) {
	var opens atomic.Int32
	address, trusted := testListener(t, pingInterval, tcp.Sessions{Open: func(context.Context, tcp.Far) (tcp.Near, error) {
		opens.Add(1)
		return tcp.Near{}, errors.New("unexpected open")
	}})
	wrongName := trusted.Clone()
	wrongName.ServerName = "another.invalid"
	for _, tc := range []struct {
		name  string
		tls   *tls.Config
		token string
	}{
		{"untrusted CA", &tls.Config{RootCAs: x509.NewCertPool()}, "pair"},
		{"wrong certificate name", wrongName, "pair"},
		{"wrong client token", trusted, "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, delay, err := dial(t.Context(), address, tc.token, tc.tls)
			if conn != nil || err == nil || delay != maxDelay {
				t.Fatalf("accepted or unexpected retry: %v, %v", delay, err)
			}
		})
	}
	d := websocket.Dialer{TLSClientConfig: trusted, Subprotocols: []string{"rutis.4"}}
	_, response, err := d.Dial(address, http.Header{"Authorization": {"Bearer pair"}})
	if err == nil || response == nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("protocol refusal: %v, %v", response, err)
	}
	response.Body.Close()
	if opens.Load() != 0 {
		t.Fatal("a refused connection opened a session")
	}
}

func TestMessageBoundariesAndClose(t *testing.T) {
	opened := make(chan tcp.Far, 1)
	received := make(chan []byte, 2)
	ended := make(chan tcp.CloseReason, 1)
	address, identity := testListener(t, pingInterval, tcp.Sessions{Open: func(_ context.Context, far tcp.Far) (tcp.Near, error) {
		opened <- far
		return tcp.Near{
			Receive: func(_ context.Context, data []byte) error { received <- data; return nil },
			End:     func(_ context.Context, reason tcp.CloseReason) error { ended <- reason; return nil },
		}, nil
	}})
	connect := func() *websocket.Conn {
		t.Helper()
		handshake, cancel := context.WithCancel(t.Context())
		conn, _, err := dial(handshake, address, "pair", identity)
		cancel() // The caller owns the socket after a successful Upgrade.
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		return conn
	}
	conn := connect()
	far := <-opened
	if extra, _, err := dial(t.Context(), address, "pair", identity); extra != nil || err == nil {
		t.Fatal("second connection admitted on occupied row")
	}
	for _, msg := range []string{"one", "two 雪"} {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
			t.Fatal(err)
		}
		select {
		case data := <-received:
			if string(data) != msg {
				t.Fatalf("received %q", data)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("missing session message")
		}
		if err := far.Send([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		kind, data, err := conn.ReadMessage()
		if err != nil || kind != websocket.TextMessage || string(data) != msg {
			t.Fatal(kind, string(data), err)
		}
	}
	if err := conn.WriteControl(websocket.CloseMessage, closeFrame(tcp.CloseReason{Kind: "rejected", Reason: "not admitted"}), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case reason := <-ended:
		if reason.Kind != "rejected" || reason.Reason != "not admitted" {
			t.Fatal(reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing end notification")
	}
}

func TestLocalCloseAndOversize(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(tcp.Far) error
		code int
	}{
		{"replace", func(far tcp.Far) error { far.Close(tcp.CloseReason{Kind: "replaced"}); return nil }, 4002},
		{"oversize", func(far tcp.Far) error { return far.Send(bytes.Repeat([]byte{'a'}, maxMessage+1)) }, websocket.CloseAbnormalClosure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened := make(chan tcp.Far, 1)
			address, identity := testListener(t, pingInterval, tcp.Sessions{Open: func(_ context.Context, far tcp.Far) (tcp.Near, error) {
				opened <- far
				return tcp.Near{End: func(context.Context, tcp.CloseReason) error { return nil }}, nil
			}})
			conn, _, err := dial(t.Context(), address, "pair", identity)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			err = tc.send(<-opened)
			if tc.name == "oversize" && err == nil {
				t.Fatal("oversized send succeeded")
			}
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, _, err = conn.ReadMessage()
			if !websocket.IsCloseError(err, tc.code) {
				t.Fatal(err)
			}
		})
	}
}
