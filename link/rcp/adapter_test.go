package rcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"github.com/gorilla/websocket"
)

func wsURL(url string) string { return "ws" + strings.TrimPrefix(url, "http") }

func TestAdmissionAndRelayFailures(t *testing.T) {
	for status, want := range map[int]failure{401: credential, 403: credential, 410: expired, 404: offline, 503: unavailable, 400: refused} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		_, err := dial(t.Context(), wsURL(srv.URL)+"?ticket=private")
		srv.Close()
		if err != want {
			t.Errorf("HTTP %d: %v", status, err)
		}
	}
	for _, tc := range []struct {
		name       string
		mutate     func(*Credentials)
		badVersion bool
		want       failure
	}{
		{"wrong peer key", func(c *Credentials) { c.PeerKey = bytes.Repeat([]byte{1}, 32) }, false, credential},
		{"different credential binding", func(c *Credentials) { c.Binding = []byte("other") }, false, credential},
		{"version mismatch", func(*Credentials) {}, true, incompatible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, daemon := credentials(t)
			tc.mutate(&daemon)
			result := make(chan error, 1)
			d := device{cfg: config{MaxMessage: 1000, HeartbeatMs: 500}, sessions: tcp.Sessions{Open: func(context.Context, tcp.Far) (tcp.Near, error) {
				t.Error("opened before admission")
				return tcp.Near{}, errors.New("unexpected")
			}}}
			up := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := up.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				_, err = d.connected(t.Context(), t.Context(), conn, daemon)
				result <- err
			}))
			defer srv.Close()
			conn, err := dial(t.Context(), wsURL(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if tc.badVersion {
				conn.WriteMessage(websocket.BinaryMessage, []byte("RCP\x00"))
				kind, msg, err := conn.ReadMessage()
				if err != nil || kind != websocket.BinaryMessage || string(msg) != rejectVersion {
					t.Fatal(err)
				}
			} else {
				_, _ = handshake(conn, client, 1000, time.Second)
			}
			if got := <-result; got != tc.want {
				t.Fatal(got)
			}
		})
	}
}

func TestCredentialRefreshAndCancellation(t *testing.T) {
	client, _ := credentials(t)
	path := filepath.Join(t.TempDir(), "grant.json")
	store := func(c Credentials) {
		b, _ := json.Marshal(c)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	stale := client
	stale.ExpiresAt = time.Now().Add(-time.Minute)
	store(stale)
	p := fileProvider(path)
	var attempts atomic.Int32
	good := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(good); w.WriteHeader(503) }))
	defer srv.Close()
	client.Address = wsURL(srv.URL)
	d := device{cfg: config{LinkID: "refresh"}, provide: func(ctx context.Context, id string) (Credentials, error) {
		if attempts.Add(1) == 2 {
			store(client)
		}
		return p(ctx, id)
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- d.runDial(ctx, ctx) }()
	select {
	case <-good:
	case <-time.After(3 * time.Second):
		t.Fatal("new credential never used")
	}
	cancel()
	if err := <-result; err != nil || attempts.Load() != 2 {
		t.Fatal(err, attempts.Load())
	}
	d.provide = func(context.Context, string) (Credentials, error) {
		return Credentials{}, errors.New("private-provider-detail")
	}
	if err := d.runDial(t.Context(), t.Context()); err != credential {
		t.Fatal("expected terminal rejection", err)
	}
}

func TestOrderedDeliveryBackpressureAndLimit(t *testing.T) {
	client, daemon := credentials(t)
	frames := make(chan []byte, 3)
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ended := make(chan tcp.CloseReason, 1)
	d := device{cfg: config{MaxMessage: 128, HeartbeatMs: 100}, sessions: tcp.Sessions{Open: func(context.Context, tcp.Far) (tcp.Near, error) {
		return tcp.Near{Receive: func(ctx context.Context, b []byte) error {
			frames <- b
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, End: func(_ context.Context, r tcp.CloseReason) error { ended <- r; return nil }}, nil
	}}}
	up := websocket.Upgrader{}
	serverDone := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := up.Upgrade(w, r, nil)
		if e == nil {
			_, e = d.connected(ctx, ctx, c, daemon)
		}
		serverDone <- e
	}))
	defer srv.Close()
	conn, err := dial(ctx, wsURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ch, err := handshake(conn, client, 64, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	one, two := bytes.Repeat([]byte("A"), 64), []byte("B")
	if err = ch.write(dataMessage, one); err != nil {
		t.Fatal(err)
	}
	if err = ch.write(dataMessage, two); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(<-frames, one) {
		t.Fatal("first message")
	}
	select {
	case <-frames:
		t.Fatal("read ahead of slow consumer")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if !bytes.Equal(<-frames, two) {
		t.Fatal("second message")
	}
	if err = ch.write(dataMessage, make([]byte, 65)); err != oversized {
		t.Fatal(err)
	}
	if err = ch.write(closeReplaced, nil); err != nil {
		t.Fatal(err)
	}
	if got := <-serverDone; got != replaced {
		t.Fatal(got)
	}
	if r := <-ended; r.Kind != "replaced" {
		t.Fatal(r)
	}
}

func TestSilentPeerAndRefusedClose(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close bool
		want  failure
	}{
		{"silent admitted peer", false, transport},
		{"authenticated refusal", true, refused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, daemon := credentials(t)
			result := make(chan error, 1)
			ended := make(chan tcp.CloseReason, 1)
			sessions := tcp.Sessions{Open: func(context.Context, tcp.Far) (tcp.Near, error) {
				return tcp.Near{End: func(_ context.Context, r tcp.CloseReason) error { ended <- r; return nil }}, nil
			}}
			d := device{cfg: config{MaxMessage: 1024, HeartbeatMs: 100}, sessions: sessions}
			up := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := up.Upgrade(w, r, nil)
				if err == nil {
					_, err = d.connected(t.Context(), t.Context(), c, daemon)
				}
				result <- err
			}))
			defer srv.Close()
			conn, err := dial(t.Context(), wsURL(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			ch, err := handshake(conn, client, 1024, 100*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			if tc.close {
				if err := ch.write(closeRejected, nil); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-result:
				if err != tc.want {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("connection remained active")
			}
			if r := <-ended; tc.close && r.Kind != "rejected" {
				t.Fatal(r)
			}
		})
	}
}
