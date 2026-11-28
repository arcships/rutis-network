package rcp

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const probeText = "visible-only-at-the-rutis-endpoints"

func TestHosts(t *testing.T) {
	host := os.Getenv("RUTIS_HOST")
	if testing.Short() || host == "" || runtime.GOOS == "windows" {
		t.Skip("requires RUTIS_HOST and Unix control sockets")
	}
	dir := t.TempDir()
	// Unix control socket paths must also fit when macOS TMPDIR is deeply nested.
	short, err := os.MkdirTemp("/tmp", "rcph-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	bin := filepath.Join(dir, "go")
	for name, src := range map[string]string{"link-rcp": "./cmd/link-rcp", "rcp-witness": "./testdata/witness"} {
		if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, name), src).CombinedOutput(); err != nil {
			t.Fatalf("go build: %s %v", out, err)
		}
	}
	listen, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := "ws://" + listen.Addr().String() + "/pair"
	listen.Close()
	client, daemon := credentials(t)

	deskListen := daemon
	client.Address = address
	remote, deskRelay := credentials(t)
	remoteKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	remote.PrivateKey = remoteKey.Bytes()
	deskRelay.PeerKey = remoteKey.PublicKey().Bytes()
	relay, packets := testRelay(t, deskRelay.PeerKey)
	remote.Binding = []byte("second-grant")
	deskRelay.Binding = remote.Binding
	remote.Address = relay + "/client?ticket=test-private-ticket"
	deskRelay.Address = relay + "/daemon?ticket=test-private-ticket"
	type obj = map[string]any
	row := func(id string, cred Credentials, listen string) obj {
		path := filepath.Join(dir, id+".json")
		writeJSON(t, path, cred)
		return obj{"id": id, "name": "go:link-rcp", "config": obj{"linkId": id, "credentialFile": path, "listen": listen, "heartbeatMs": 1000}}
	}
	directRow := row("direct", deskListen, address)
	relayRow := row("via-relay", deskRelay, "")
	manifest := func(id string, rows ...obj) string {
		home := filepath.Join(short, id)
		if err := os.MkdirAll(home, 0700); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, obj{"id": "app", "name": "go:rcp-witness", "config": obj{"service": id}})
		path := filepath.Join(home, "rutis.json")
		writeJSON(t, path, obj{"id": id, "runtimes": obj{"go": obj{"dir": bin}}, "rows": rows})
		return path
	}
	start := func(path string) func() {
		run := exec.Command(host, "run", "--watch-config", path)
		log, err := os.CreateTemp(dir, "host-log-")
		if err != nil {
			t.Fatal(err)
		}
		run.Stdout = log
		run.Stderr = log
		if err := run.Start(); err != nil {
			t.Fatal(err)
		}
		stop := sync.OnceFunc(func() {
			run.Process.Signal(os.Interrupt)
			waited := make(chan error, 1)
			go func() { waited <- run.Wait() }()
			select {
			case err := <-waited:
				if err != nil {
					t.Errorf("host shutdown: %v", err)
				}
			case <-time.After(15 * time.Second):
				run.Process.Kill()
				<-waited
				t.Error("host failed to drain")
			}
			log.Close()
			b, _ := os.ReadFile(log.Name())
			if t.Failed() {
				t.Log(string(b))
			}
			for _, secret := range []string{probeText, "test-private-ticket", base64.StdEncoding.EncodeToString(client.PrivateKey), base64.StdEncoding.EncodeToString(daemon.PrivateKey)} {
				if bytes.Contains(b, []byte(secret)) {
					t.Error("host output contains test secret or plaintext")
				}
			}
		})
		t.Cleanup(stop)
		return stop
	}
	desktop := manifest("desk", directRow, relayRow)
	start(desktop)
	a := manifest("alpha", row("to-desk-a", client, ""))
	stopA := start(a)
	b := manifest("beta", row("to-desk-b", remote, ""))
	start(b)
	call := func(path, method, arg string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, host, "call", "--config", path, method, arg).CombinedOutput()
	}
	echo := func(path, peer string) {
		t.Helper()
		until := time.Now().Add(18 * time.Second)
		var last []byte
		for time.Now().Before(until) {
			out, err := call(path, peer+".echo", `"`+probeText+`"`)
			last = out
			var got string
			if err == nil && json.Unmarshal(out, &got) == nil && got == peer+" "+probeText {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("service %s from %s: %.1000s", peer, path, last)
	}
	echo(a, "desk")
	echo(desktop, "alpha")
	echo(b, "desk")
	echo(desktop, "beta")
	if packets.Load() < 4 {
		t.Fatal("missing capture of relay records")
	}
	// A second connect to the occupied row must not replace the addressable session.
	if conn, err := dial(t.Context(), address); err != unavailable {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("duplicate dial: %v", err)
	}
	// rutis 0.23's FarSender does not advertise the link limit (#1411).
	// Its 17 MiB result must end only this connection. Reconnect must recover
	// the row while the second device continues serving calls.
	out, err := call(a, "desk.blob", "17825792")
	if err == nil || !bytes.Contains(out, []byte("peer disconnected")) {
		t.Fatalf("expected the documented 0.23 frame-limit failure: %v %.300s", err, out)
	}
	echo(desktop, "beta")
	echo(a, "desk")
	manifest("desk", relayRow)
	deadline := time.Now().Add(6 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", strings.TrimPrefix(strings.TrimSuffix(address, "/pair"), "ws://"), 100*time.Millisecond)
		if err != nil {
			break
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatal("removed row kept its socket")
		}
		time.Sleep(50 * time.Millisecond)
	}
	echo(desktop, "beta")
	manifest("desk", directRow, relayRow)
	echo(a, "desk")
	stopA()
	start(a)
	echo(desktop, "alpha")
	echo(desktop, "beta")
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path+".tmp", b, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}

// The relay owns sockets and routing labels only. Inspect the actual data after
// the outer WebSocket is decoded, before forwarding it to the other endpoint.
func testRelay(t *testing.T, publicClientKey []byte) (string, *atomic.Int64) {
	var packets atomic.Int64
	upgrader := websocket.Upgrader{}
	type pair struct {
		sockets map[string]*websocket.Conn
		ready   chan struct{}
	}
	newPair := func() *pair { return &pair{sockets: map[string]*websocket.Conn{}, ready: make(chan struct{})} }
	current := newPair()
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(t.Context())
	// Give the observer public routing context and the real client's public key,
	// but only its own private key. Authentication of the captured offer must fail.
	observerKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	observer := Credentials{Role: "daemon", Binding: []byte("second-grant"), PrivateKey: observerKey.Bytes(), PeerKey: publicClientKey}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := strings.TrimPrefix(r.URL.Path, "/")
		if (role != "client" && role != "daemon") || r.URL.Query().Get("ticket") != "test-private-ticket" {
			w.WriteHeader(403)
			return
		}
		mu.Lock()
		group := current
		if group.sockets[role] != nil {
			mu.Unlock()
			w.WriteHeader(503)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			mu.Unlock()
			return
		}
		group.sockets[role] = conn
		if len(group.sockets) == 2 {
			close(group.ready)
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			if current == group {
				current = newPair()
				for _, c := range group.sockets {
					c.Close()
				}
			}
			mu.Unlock()
		}()
		select {
		case <-group.ready:
		case <-ctx.Done():
			return
		}
		other := "client"
		if role == "client" {
			other = "daemon"
		}
		dst := group.sockets[other]
		for {
			kind, b, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if bytes.Contains(b, []byte(probeText)) || bytes.Contains(b, []byte("rutis/sessions")) {
				t.Error("plaintext in relay capture")
				return
			}
			if kind != websocket.BinaryMessage {
				t.Error("unexpected relay frame type")
				return
			}
			if bytes.HasPrefix(b, []byte(version)) {
				if role == "client" {
					state, err := pairState(observer, nil)
					if err != nil {
						t.Error(err)
						return
					}
					if _, _, _, err = state.ReadMessage(nil, b[len(version):]); err == nil {
						t.Error("relay authenticated encrypted handshake payload")
						return
					}
				}
			} else {
				if len(b) < overhead {
					t.Error("truncated encrypted record")
					return
				}
				packets.Add(1)
			}
			if dst.WriteMessage(kind, b) != nil {
				return
			}
		}
	}))
	t.Cleanup(func() {
		cancel()
		mu.Lock()
		for _, c := range current.sockets {
			c.Close()
		}
		mu.Unlock()
		srv.Close()
	})
	return wsURL(srv.URL), &packets
}
