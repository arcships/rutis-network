package wslink

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"github.com/gorilla/websocket"
)

func TestHandshakeHeaderForms(t *testing.T) {
	for _, tc := range []struct {
		name            string
		auth, protocols []string
		want            int
	}{
		{"lowercase scheme", []string{"bearer pair"}, []string{protocol}, 101},
		{"mixed case and spaces", []string{"BeArEr   pair"}, []string{protocol}, 101},
		{"separate protocol fields", []string{"Bearer pair"}, []string{"rutis.3, rutis.4", protocol}, 101},
		{"token stays case sensitive", []string{"bearer PAIR"}, []string{protocol}, 403},
		{"duplicate authorization", []string{"Bearer pair", "Bearer pair"}, []string{protocol}, 403},
		{"tab separator", []string{"Bearer\tpair"}, []string{protocol}, 403},
		{"no supported protocol", []string{"Bearer pair"}, []string{"rutis.3", "rutis.4"}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			address, trusted := testListener(t, pingInterval, tcp.Sessions{Open: func(context.Context, tcp.Far) (tcp.Near, error) {
				return tcp.Near{}, errors.New("fixture complete")
			}})
			d := websocket.Dialer{TLSClientConfig: trusted}
			conn, resp, err := d.DialContext(t.Context(), address, http.Header{
				"Authorization": tc.auth, "Sec-WebSocket-Protocol": tc.protocols,
			})
			if conn != nil {
				defer conn.Close()
			}
			if resp == nil {
				t.Fatalf("missing upgrade response: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("got HTTP %d, want %d: %v", resp.StatusCode, tc.want, err)
			}
			if tc.want == http.StatusSwitchingProtocols && (err != nil || conn == nil || conn.Subprotocol() != protocol) {
				t.Fatalf("expected %s negotiation: %v", protocol, err)
			}
		})
	}
}

func TestCancelPendingUpgrade(t *testing.T) {
	arrived, disconnected, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(arrived)
		select {
		case <-r.Context().Done():
			close(disconnected)
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		conn, _, err := dial(ctx, "wss"+strings.TrimPrefix(server.URL, "https"), "pair", &tls.Config{RootCAs: roots})
		if conn != nil {
			_ = conn.Close()
		}
		result <- err
	}()
	select {
	case <-arrived:
	case err := <-result:
		t.Fatalf("dial ended before Upgrade reached the listener: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("missing Upgrade request")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected dial result: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not interrupt the pending Upgrade")
	}
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled handshake left its socket open")
	}
}
