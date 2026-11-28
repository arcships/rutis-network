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

	"github.com/arcships/rutis/go/rutis/rutistest"
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
	relay := testRelay(t, deskRelay.PeerKey)
	remote.Binding = []byte("second-grant")
	deskRelay.Binding = remote.Binding
	remote.Address = relay.url + "/client?ticket=test-private-ticket"
	deskRelay.Address = relay.url + "/daemon?ticket=test-private-ticket"
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
	b := manifest("beta", row("to-desk-b", remote, ""))
	start(b)
	select {
	case <-relay.droppedOffer:
	case <-time.After(10 * time.Second):
		t.Fatal("client did not offer before the daemon joined")
	}
	desktop := manifest("desk", directRow, relayRow)
	stopDesktop := start(desktop)
	a := manifest("alpha", row("to-desk-a", client, ""))
	stopA := start(a)
	call := func(path, method, arg string, timeout time.Duration) ([]byte, error) {
		ctx, cancel := context.WithTimeout(t.Context(), timeout)
		defer cancel()
		return exec.CommandContext(ctx, host, "call", "--config", path, method, arg).CombinedOutput()
	}
	echo := func(path, peer string) {
		t.Helper()
		until := time.Now().Add(18 * time.Second)
		var last []byte
		for time.Now().Before(until) {
			out, err := call(path, peer+".echo", `"`+probeText+`"`, 10*time.Second)
			last = out
			var got string
			if err == nil && json.Unmarshal(out, &got) == nil && got == peer+" "+probeText {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("service %s from %s: %.1000s", peer, path, last)
	}
	waitForRemoval := func(path, peer string) {
		t.Helper()
		deadline := time.Now().Add(18 * time.Second)
		for {
			out, err := call(path, peer+".echo", `""`, 3*time.Second)
			if err != nil && bytes.Contains(out, []byte("no row provides "+peer+" ")) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("service %s still registered on %s: %v %.300s", peer, path, err, out)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	echo(a, "desk")
	echo(desktop, "alpha")
	echo(b, "desk")
	echo(desktop, "beta")
	for _, role := range []string{"client", "daemon"} {
		relay.disconnect(role)
		echo(b, "desk")
		echo(desktop, "beta")
	}
	if relay.packets.Load() < 4 {
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
	for _, request := range []struct{ from, method string }{{a, "desk.blob"}, {desktop, "alpha.blob"}} {
		out, err := call(request.from, request.method, "17825792", 45*time.Second)
		if err == nil || !bytes.Contains(out, []byte("peer disconnected")) {
			t.Fatalf("%s: expected the 0.23 frame-limit failure: %v %.300s", request.method, err, out)
		}
		echo(desktop, "beta")
		echo(a, "desk")
		echo(desktop, "alpha")
	}
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
	waitForRemoval(desktop, "alpha")
	start(a)
	echo(a, "desk")
	echo(desktop, "alpha")
	echo(desktop, "beta")
	// Shut the two-session host down while both peers can still acknowledge
	// its teardown. Each live peer must withdraw the desk service afterwards.
	stopDesktop()
	waitForRemoval(a, "desk")
	waitForRemoval(b, "desk")
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

type relayFixture struct {
	url          string
	packets      atomic.Int64
	droppedOffer chan struct{}
	disconnect   func(string)
}

// Each relay slot has its own lifetime. Frames sent before the other role
// joins are dropped, and replacing one role leaves the other socket alive.
func testRelay(t *testing.T, publicClientKey []byte) *relayFixture {
	relay := &relayFixture{droppedOffer: make(chan struct{})}
	markDropped := sync.OnceFunc(func() { close(relay.droppedOffer) })
	var duplicateOffer sync.Once
	upgrader := websocket.Upgrader{}
	sockets := map[string]*websocket.Conn{}
	var mu sync.Mutex
	relay.disconnect = func(role string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if sockets[role] == nil {
			t.Fatalf("missing %s relay slot", role)
		}
		sockets[role].Close()
		delete(sockets, role)
	}
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
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		if previous := sockets[role]; previous != nil {
			previous.Close()
		}
		sockets[role] = conn
		mu.Unlock()
		defer func() {
			mu.Lock()
			defer mu.Unlock()
			conn.Close()
			if sockets[role] == conn {
				delete(sockets, role)
			}
		}()
		other := "client"
		if role == "client" {
			other = "daemon"
		}
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
				relay.packets.Add(1)
			}
			mu.Lock()
			if dst := sockets[other]; dst != nil && sockets[role] == conn {
				dst.SetWriteDeadline(time.Now().Add(time.Second))
				if err := dst.WriteMessage(kind, b); err != nil {
					dst.Close()
				} else if role == "client" && isPair(b) {
					// Deliver one duplicate before READY, as on a delayed pairing response.
					duplicateOffer.Do(func() {
						if dst.WriteMessage(kind, b) != nil {
							dst.Close()
						}
					})
				}
			} else if role == "client" && isPair(b) {
				markDropped()
			}
			mu.Unlock()
		}
	}))
	t.Cleanup(func() {
		mu.Lock()
		for _, c := range sockets {
			c.Close()
		}
		mu.Unlock()
		srv.Close()
	})
	relay.url = wsURL(srv.URL)
	return relay
}

func TestListenerSeparatesIssuerOutageFromDenial(t *testing.T) {
	if testing.Short() || os.Getenv("RUTIS_HOST") == "" || runtime.GOOS == "windows" {
		t.Skip("requires RUTIS_HOST and Unix control sockets")
	}
	socket, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := "ws://" + socket.Addr().String() + "/pair"
	socket.Close()
	var revoked atomic.Bool
	provide := func(context.Context, string) (Credentials, error) {
		if revoked.Load() {
			return Credentials{}, credential
		}
		return Credentials{}, ErrProviderUnavailable
	}
	rutistest.Load(t, Plugin(provide), config{LinkID: "test", Listen: address}, nil)
	for _, want := range []failure{unavailable, credential} {
		if conn, err := dial(t.Context(), address); err != want {
			if conn != nil {
				conn.Close()
			}
			t.Fatalf("got %v, want %v", err, want)
		}
		revoked.Store(true)
	}
}
