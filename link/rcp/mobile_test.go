package rcp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"github.com/gorilla/websocket"
)

// Both processes exchange a distinct message and acknowledge the other's message.
func TestMobileInterop(t *testing.T) {
	if os.Getenv("RCP_KOTLIN_CLASSPATH") == "" {
		t.Skip("use make integration-rcp-mobile to build the mobile test peers")
	}
	for _, pair := range []struct{ client, daemon string }{{"kotlin", "go"}, {"swift", "go"}, {"swift", "kotlin"}} {
		t.Run(pair.client+"-"+pair.daemon, func(t *testing.T) {
			for _, path := range []string{"direct", "relay"} {
				t.Run(path, func(t *testing.T) { mobileExchange(t, pair.client, pair.daemon, path) })
			}
		})
	}
}

func mobileExchange(t *testing.T, mobile, server, path string) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	client, daemon := credentials(t)
	client.Binding, daemon.Binding = []byte("second-grant"), []byte("second-grant")
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	run := func(language, mode string, c Credentials) <-chan error {
		return mobilePeer(t, ctx, language, mobileFixture{Credentials: c, Mode: mode, Ready: ready})
	}
	if path == "relay" {
		relay := testRelay(t, daemon.PeerKey)
		client.Address = relay.url + "/client?ticket=test-private-ticket"
		daemon.Address = relay.url + "/daemon?ticket=test-private-ticket"
	}
	var serverDone <-chan error
	if server == "go" {
		done := make(chan error, 1)
		serverDone = done
		d := device{cfg: config{MaxMessage: 4096, HeartbeatMs: 150}, sessions: tcp.Sessions{
			Open: func(_ context.Context, far tcp.Far) (tcp.Near, error) {
				greeted, acknowledged := false, false
				return tcp.Near{
					Receive: func(_ context.Context, b []byte) error {
						switch string(b) {
						case probeText + "-client":
							greeted = true
							return far.Send([]byte("ack-client"))
						case "ack-daemon":
							acknowledged = true
						default:
							return errors.New("unexpected mobile message")
						}
						return nil
					},
					End: func(context.Context, tcp.CloseReason) error {
						if !greeted || !acknowledged {
							done <- errors.New("incomplete mobile exchange")
						} else {
							done <- nil
						}
						return nil
					},
				}, far.Send([]byte(probeText + "-daemon"))
			},
		}}
		serve := func(c *websocket.Conn) {
			if admitted, err := d.connected(ctx, ctx, c, daemon); !admitted {
				done <- err
			}
		}
		if path == "relay" {
			conn, err := dial(ctx, daemon.Address)
			if err != nil {
				t.Fatal(err)
			}
			go serve(conn)
		} else {
			up := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if conn, err := up.Upgrade(w, r, nil); err == nil {
					serve(conn)
				}
			}))
			t.Cleanup(srv.Close)
			client.Address = wsURL(srv.URL)
		}
	} else if path == "direct" {
		serverDone = run(server, "listen", daemon)
		for client.Address == "" {
			if b, _ := os.ReadFile(ready); len(b) > 0 {
				client.Address = string(b)
				break
			}
			select {
			case err := <-serverDone:
				t.Fatalf("listener exited before binding: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
	} else {
		serverDone = run(server, "dial", daemon)
	}
	clientDone := run(mobile, "dial", client)
	for clientDone != nil || serverDone != nil {
		select {
		case err := <-clientDone:
			if err != nil {
				t.Fatalf("client: %v", err)
			}
			clientDone = nil
		case err := <-serverDone:
			if err != nil {
				t.Fatalf("daemon: %v", err)
			}
			serverDone = nil
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

type mobileFixture struct {
	Credentials
	Mode   string `json:"mode"`
	Ready  string `json:"readyFile"`
	Fault  string `json:"fault"`
	Reason string `json:"reason"`
}

func mobilePeer(t *testing.T, ctx context.Context, language string, fixture mobileFixture) <-chan error {
	t.Helper()
	body, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), fixture.Role+".json")
	if err := os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	var cmd *exec.Cmd
	if language == "kotlin" {
		cmd = exec.CommandContext(ctx, "java", "-cp", os.Getenv("RCP_KOTLIN_CLASSPATH"), "arcships.rcp.InteropPeerKt")
	} else {
		cmd = exec.CommandContext(ctx, "swift", "test", "--skip-build", "--filter", "InteropTests.testWebSocketPeer")
		cmd.Dir = "swift"
	}
	cmd.Env = append(os.Environ(), "RCP_FIXTURE="+file)
	done := make(chan error, 1)
	go func() {
		out, err := cmd.CombinedOutput()
		if err != nil {
			done <- errors.New(err.Error() + "\n" + string(out))
			return
		}
		if language == "swift" && !bytes.Contains(out, []byte("Executed 1 test, with 0 failures")) {
			done <- errors.New("Swift peer did not run: " + string(out))
			return
		}
		done <- nil
	}()
	return done
}

// Send an authenticated 129-byte record after both Noise offers, before the daemon's READY.
func oversizedBeforeReady(conn *websocket.Conn, daemon Credentials) error {
	state, err := pairState(daemon, nil)
	if err != nil {
		return err
	}
	initial, err := receivePair(conn, nil)
	if err != nil {
		return err
	}
	if _, _, _, err := state.ReadMessage(nil, initial[len(version):]); err != nil {
		return err
	}
	reply, _, tx, err := state.WriteMessage(nil, offer(4096, 150*time.Millisecond))
	if err != nil {
		return err
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, append([]byte(version), reply...)); err != nil {
		return err
	}
	if _, err := readHandshakeFrame(conn); err != nil {
		return err
	}
	frame, err := seal(tx, dataMessage, make([]byte, 129-overhead))
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.BinaryMessage, frame)
}

func writeFragments(conn *websocket.Conn, frame []byte) error {
	w, err := conn.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return err
	}
	for len(frame) > 0 {
		n := min(192, len(frame))
		if _, err := w.Write(frame[:n]); err != nil {
			return err
		}
		frame = frame[n:]
	}
	return w.Close()
}

// Exercise both WebSocket length encodings used for a full 64 KiB RCP message,
// fragmented binary delivery, and WebSocket control frames alongside RCP traffic.
func mobileLargeEcho(ch *channel) error {
	ch.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	observedPong := false
	ch.conn.SetPongHandler(func(s string) error { observedPong = s == "check"; return nil })
	if err := ch.conn.WriteControl(websocket.PingMessage, []byte("check"), time.Now().Add(time.Second)); err != nil {
		return err
	}
	for i := byte(1); i <= 2; i++ {
		message := bytes.Repeat([]byte{i}, 65536)
		frame, err := seal(ch.send, dataMessage, message)
		if err != nil {
			return err
		}
		if i == 1 {
			err = ch.conn.WriteMessage(websocket.BinaryMessage, frame)
		} else {
			err = writeFragments(ch.conn, frame)
		}
		if err != nil {
			return err
		}
		for {
			kind, reply, err := ch.read()
			if err != nil {
				return err
			}
			if kind == ping {
				if err = ch.write(pong, nil); err != nil {
					return err
				}
				continue
			}
			if kind != dataMessage || !bytes.Equal(message, reply) {
				return errors.New("incorrect large message echo")
			}
			break
		}
	}
	for {
		kind, _, err := ch.read()
		if err != nil {
			return err
		}
		if kind == closeDone {
			break
		}
		if kind != ping {
			return errors.New("missing close after large message echo")
		}
		if err := ch.write(pong, nil); err != nil {
			return err
		}
	}
	if !observedPong {
		return errors.New("missing WebSocket pong")
	}
	return nil
}

func TestMobileFaults(t *testing.T) {
	if os.Getenv("RCP_KOTLIN_CLASSPATH") == "" {
		t.Skip("use make integration-rcp-mobile")
	}
	for _, lang := range []string{"kotlin", "swift"} {
		t.Run(lang, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				status int
				reason failure
			}{
				{"version", 0, incompatible}, {"revoked", 403, credential}, {"invalid", 0, credential},
				{"unavailable", 503, unavailable}, {"offline", 404, offline}, {"expired", 0, expired},
				{"silent", 0, transport}, {"slow", 0, replaced}, {"refused", 0, refused}, {"oversize", 0, oversized},
				{"before-ready-size", 0, oversized}, {"inbound-size", 0, oversized}, {"negotiated-size", 0, oversized}, {"close-1009", 0, oversized},
				{"large", 0, done}, {"local-replaced", 0, replaced}, {"local-rejected", 0, refused},
			} {
				t.Run(tc.name, func(t *testing.T) {
					client, daemon := credentials(t)
					up := websocket.Upgrader{WriteBufferSize: 256}
					peerClose := make(chan byte, 1)
					var attempts atomic.Int32
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						first := attempts.Add(1) == 1
						if tc.status != 0 && (first || tc.name == "revoked") {
							w.WriteHeader(tc.status)
							return
						}
						conn, err := up.Upgrade(w, r, nil)
						if err != nil {
							return
						}
						defer conn.Close()
						if tc.name == "version" {
							if _, _, err := conn.ReadMessage(); err == nil {
								_ = conn.WriteMessage(websocket.BinaryMessage, []byte(rejectVersion))
							}
							return
						}
						if tc.name == "before-ready-size" && first {
							if err := oversizedBeforeReady(conn, daemon); err != nil {
								t.Error(err)
							}
							_, _, _ = conn.ReadMessage()
							return
						}
						limit := 4096
						if tc.name == "large" {
							limit = 65536
						}
						ch, err := handshake(conn, daemon, limit, 150*time.Millisecond)
						if err != nil {
							if err == credential {
								_ = conn.WriteMessage(websocket.BinaryMessage, []byte(rejectCredential))
							}
							return
						}
						switch tc.name {
						case "large":
							if err := mobileLargeEcho(ch); err != nil {
								t.Error(err)
							}
							return
						case "local-replaced", "local-rejected":
							kind, _, err := ch.read()
							if err == nil && first {
								peerClose <- kind
							}
							return
						case "negotiated-size":
							if first {
								// Send only the header. A reader that buffers the body will time out.
								header := binary.BigEndian.AppendUint16([]byte{0x82, 126}, uint16(ch.limit+overhead+1))
								if _, err := conn.UnderlyingConn().Write(header); err != nil {
									t.Error(err)
								}
								conn.SetReadDeadline(time.Now().Add(3 * time.Second))
								for {
									if _, _, err := ch.read(); err != nil {
										return
									}
								}
							}
						case "inbound-size":
							if first {
								// Each fragment fits; the cumulative payload exceeds the negotiated limit by one.
								frame, err := seal(ch.send, dataMessage, make([]byte, ch.limit+1))
								if err == nil {
									err = writeFragments(ch.conn, frame)
								}
								if err != nil {
									t.Error(err)
								}
							}
						case "close-1009":
							if first {
								_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseMessageTooBig, ""), time.Now().Add(time.Second))
								_, _, _ = conn.ReadMessage()
								return
							}
						}
						if tc.name == "silent" && first {
							conn.SetReadDeadline(time.Now().Add(3 * time.Second))
							for {
								if _, _, err := ch.read(); err != nil {
									return
								}
							}
						}
						if tc.name == "refused" || (tc.name == "oversize" && first) {
							if err := ch.write(dataMessage, []byte("probe")); err != nil {
								return
							}
						}
						if tc.name == "slow" {
							for _, b := range []byte{1, 2, 3} {
								if err := ch.write(dataMessage, bytes.Repeat([]byte{b}, 4096)); err != nil {
									return
								}
							}
							_ = ch.write(closeReplaced, nil)
							// Let the slow receiver drain the close record. Its next
							// heartbeat does not acknowledge that it has read it.
							conn.SetReadDeadline(time.Now().Add(3 * time.Second))
							for {
								if _, _, err := ch.read(); err != nil {
									return
								}
							}
						}
						_, _, _ = ch.read()
					}))
					defer srv.Close()
					client.Address = wsURL(srv.URL)
					ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
					defer cancel()
					if err := <-mobilePeer(t, ctx, lang, mobileFixture{Credentials: client, Mode: "fault", Fault: tc.name, Reason: string(tc.reason)}); err != nil {
						t.Fatal(err)
					}
					if tc.name == "local-replaced" || tc.name == "local-rejected" {
						want := closeReplaced
						if tc.name == "local-rejected" {
							want = closeRejected
						}
						select {
						case got := <-peerClose:
							if got != want {
								t.Fatalf("Go received close kind %d, want %d", got, want)
							}
						case <-ctx.Done():
							t.Fatal("Go did not receive encrypted close")
						}
					}
				})
			}
		})
	}
}

// Flushing the final encrypted record over WSS must finish before local teardown.
func TestMobileKotlinWSSClose(t *testing.T) {
	if os.Getenv("RCP_KOTLIN_CLASSPATH") == "" {
		t.Skip("use make integration-rcp-mobile")
	}
	client, daemon := credentials(t)
	up := websocket.Upgrader{}
	result := make(chan error, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		ch, err := handshake(conn, daemon, 4096, 150*time.Millisecond)
		if err != nil {
			result <- err
			return
		}
		kind, _, err := ch.read()
		if err == nil && kind != closeRejected {
			err = errors.New("Go did not receive closeRejected over WSS")
		}
		result <- err
	}))
	defer srv.Close()
	dir := t.TempDir()
	cert, store := filepath.Join(dir, "server.der"), filepath.Join(dir, "trust.p12")
	if err := os.WriteFile(cert, srv.Certificate().Raw, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("keytool", "-importcert", "-noprompt", "-alias", "fixture",
		"-keystore", store, "-storetype", "PKCS12", "-storepass", "fixture", "-file", cert).CombinedOutput(); err != nil {
		t.Fatalf("create test trust store: %v: %s", err, out)
	}
	t.Setenv("JAVA_TOOL_OPTIONS", os.Getenv("JAVA_TOOL_OPTIONS")+` "-Djavax.net.ssl.trustStore=`+store+`" -Djavax.net.ssl.trustStorePassword=fixture`)
	client.Address = "wss" + strings.TrimPrefix(srv.URL, "https")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := <-mobilePeer(t, ctx, "kotlin", mobileFixture{Credentials: client, Mode: "fault", Fault: "local-rejected", Reason: string(refused)}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// An already-joined mobile daemon must also accept the Go initiator through a forwarding relay.
func TestMobileRelayResponder(t *testing.T) {
	if os.Getenv("RCP_KOTLIN_CLASSPATH") == "" {
		t.Skip("use make integration-rcp-mobile")
	}
	for _, lang := range []string{"swift", "kotlin"} {
		t.Run(lang, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			client, daemon := credentials(t)
			client.Binding, daemon.Binding = []byte("second-grant"), []byte("second-grant")
			relay := testRelay(t, daemon.PeerKey)
			daemon.Address = relay.url + "/daemon?ticket=test-private-ticket"
			peer := mobilePeer(t, ctx, lang, mobileFixture{Credentials: daemon, Mode: "dial"})
			conn, err := dial(ctx, relay.url+"/client?ticket=test-private-ticket")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			ch, err := handshake(conn, client, 4096, 150*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			if err := ch.write(dataMessage, []byte(probeText+"-client")); err != nil {
				t.Fatal(err)
			}
			greeted := false
			for {
				conn.SetReadDeadline(time.Now().Add(3 * time.Second))
				kind, b, err := ch.read()
				if err != nil {
					t.Fatal(err)
				}
				if kind == ping {
					if err := ch.write(pong, nil); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if kind != dataMessage {
					t.Fatalf("unexpected record %d", kind)
				}
				switch string(b) {
				case probeText + "-daemon":
					greeted = true
					if err := ch.write(dataMessage, []byte("ack-daemon")); err != nil {
						t.Fatal(err)
					}
				case "ack-client":
					if !greeted {
						t.Fatal("peer never greeted")
					}
					_ = ch.write(closeDone, nil)
				default:
					t.Fatal("unexpected message")
				}
				if string(b) == "ack-client" {
					break
				}
			}
			if err := <-peer; err != nil {
				t.Fatal(err)
			}
		})
	}
}
