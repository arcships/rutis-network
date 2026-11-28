package rcp

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"github.com/gorilla/websocket"
)

func TestGoFaultMatrix(t *testing.T) {
	for _, path := range []string{"direct", "relay"} {
		t.Run(path, func(t *testing.T) {
			offlineReason := offline
			if path == "direct" {
				offlineReason = transport // no listener to return a relay status
			}
			for _, tc := range []struct {
				name    string
				reasons []failure
			}{
				{"version_mismatch", []failure{incompatible}},
				{"invalid_grant", []failure{credential, credential}},
				{"revoked_grant", []failure{credential, credential}},
				{"unavailable_503", []failure{unavailable, done}},
				{"peer_offline", []failure{offlineReason, done}},
			} {
				t.Run(tc.name, func(t *testing.T) { checkGoFault(t, path, tc.name, tc.reasons) })
			}
		})
	}
}

func checkGoFault(t *testing.T, path, fault string, want []failure) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	grantFor := matrixGrants(t, ctx, path, fault)
	attempts := 0
	provide := func(context.Context, string) (Credentials, error) {
		attempts++
		if attempts > len(want) {
			cancel()
			return Credentials{}, errors.New("unexpected extra attempt")
		}
		return grantFor(attempts), nil
	}
	opens := 0
	var reply string
	cfg := config{LinkID: "matrix-client", MaxMessage: 1024, HeartbeatMs: 1000}
	d := device{cfg: cfg, provide: provide, sessions: tcp.Sessions{Open: func(_ context.Context, far tcp.Far) (tcp.Near, error) {
		opens++
		if attempts != len(want) || want[len(want)-1] != done {
			t.Error("session opened before admission recovered")
		}
		return tcp.Near{
			Receive: func(_ context.Context, b []byte) error {
				reply = string(b)
				far.Close(tcp.CloseReason{Kind: "done"})
				return nil
			},
			End: func(context.Context, tcp.CloseReason) error { return nil },
		}, far.Send([]byte(probeText))
	}}}
	// Observe the operator-facing close codes from the normal dial entry point.
	// These subtests run serially because the Go plugin uses the standard logger.
	var output bytes.Buffer
	previous := log.Writer()
	run := func() error {
		log.SetOutput(&output)
		defer log.SetOutput(previous)
		return d.runDial(ctx, ctx)
	}
	err := run()
	var reasons []failure
	for line := range strings.SplitSeq(output.String(), "\n") {
		if _, reason, ok := strings.Cut(line, "link=matrix-client direction=dial stage=close reason="); ok {
			reasons = append(reasons, failure(reason))
		}
	}
	if ctx.Err() != nil || attempts != len(want) || !slices.Equal(reasons, want) {
		t.Fatalf("attempts=%d close=%v ctx=%v, want %v", attempts, reasons, ctx.Err(), want)
	}
	last := want[len(want)-1]
	if last == credential && err != credential || last != credential && err != nil {
		t.Fatalf("dial task returned %v after %v", err, last)
	}
	if last == done {
		if opens != 1 || reply != "echo "+probeText {
			t.Fatalf("recovery: sessions=%d reply=%q", opens, reply)
		}
	} else if opens != 0 {
		t.Fatalf("terminal fault admitted %d sessions", opens)
	}
}

func matrixGrants(t *testing.T, ctx context.Context, path, fault string) func(attempt int) Credentials {
	client, daemon := credentials(t)
	client.Binding = []byte("second-grant")
	daemon.Binding = client.Binding
	cfg := config{LinkID: "matrix-peer", MaxMessage: 1024, HeartbeatMs: 1000}
	peer := device{cfg: cfg, sessions: tcp.Sessions{Open: func(_ context.Context, far tcp.Far) (tcp.Near, error) {
		return tcp.Near{
			Receive: func(_ context.Context, b []byte) error { return far.Send(append([]byte("echo "), b...)) },
			End:     func(context.Context, tcp.CloseReason) error { return nil },
		}, nil
	}}}
	respond := func(conn *websocket.Conn) {
		defer conn.Close()
		if fault == "version_mismatch" {
			conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, offer, err := conn.ReadMessage(); err == nil && isPair(offer) {
				_ = conn.WriteMessage(websocket.BinaryMessage, []byte(rejectVersion))
			}
			return
		}
		_, _ = peer.connected(ctx, ctx, conn, daemon)
	}
	// Join direct handlers (including hijacked sockets) and relay responders.
	var responders sync.WaitGroup
	t.Cleanup(responders.Wait)
	var directStatus atomic.Int32
	var relay *relayFixture
	var offlineAddress string
	if path == "direct" {
		up := websocket.Upgrader{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			responders.Add(1)
			defer responders.Done()
			if status := directStatus.Load(); status != 0 {
				w.WriteHeader(int(status))
				return
			}
			if conn, err := up.Upgrade(w, r, nil); err == nil {
				respond(conn)
			}
		}))
		t.Cleanup(srv.Close)
		client.Address = wsURL(srv.URL)
		if fault == "peer_offline" {
			absent := httptest.NewServer(nil)
			offlineAddress = wsURL(absent.URL)
			absent.Close()
		}
	} else {
		relay = testRelay(t, daemon.PeerKey)
		client.Address = relay.url + "/client?ticket=test-private-ticket"
		daemon.Address = relay.url + "/daemon?ticket=test-private-ticket"
	}

	return func(attempt int) Credentials {
		grant := client
		status := 0
		switch fault {
		case "invalid_grant":
			grant.Binding = []byte("wrong binding")
		case "revoked_grant":
			status = http.StatusForbidden
		case "unavailable_503":
			if attempt == 1 {
				status = http.StatusServiceUnavailable
			}
		case "peer_offline":
			if attempt == 1 {
				if relay != nil {
					status = http.StatusNotFound
				} else {
					grant.Address = offlineAddress
				}
			}
		}
		if relay == nil {
			directStatus.Store(int32(status))
		} else {
			relay.clientStatus.Store(int32(status))
			if status == 0 {
				conn, err := dial(ctx, daemon.Address)
				if err != nil {
					t.Fatalf("start relay peer: %v", err)
				}
				responders.Go(func() { respond(conn) })
			}
		}
		return grant
	}
}
