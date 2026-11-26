package cloudflared

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fixture implements cloudflared's two child-process contracts.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "access":
			if strings.Join(os.Args[2:], " ") != "tcp --hostname lab.example.com" ||
				os.Getenv("TUNNEL_SERVICE_TOKEN_ID") != "fixture-id" || os.Getenv("TUNNEL_SERVICE_TOKEN_SECRET") != "fixture-secret" ||
				os.Getenv("TUNNEL_TOKEN") != "" {
				os.Exit(2)
			}
			io.Copy(os.Stdout, os.Stdin)
			os.Exit(0)
		case "tunnel":
			args := os.Args[2:]
			if len(args) != 7 || args[0] != "--config=" || args[1] != "--no-autoupdate" || args[2] != "--metrics" || args[4] != "run" || args[5] != "--token-file" ||
				os.Getenv("TUNNEL_TOKEN") != "" || os.Getenv("TUNNEL_SERVICE_TOKEN_SECRET") != "" {
				os.Exit(2)
			}
			data, _ := os.ReadFile(args[6])
			if string(data) != "fixture-token" {
				os.Exit(3)
			}
			id := fmt.Sprintf("00000000-0000-4000-8000-%012x", os.Getpid())
			fmt.Fprintf(os.Stderr, "2026-01-01T00:00:00Z INF Generated Connector ID: %s\n", id)
			listener, err := net.Listen("tcp", args[3])
			if err != nil {
				fmt.Fprintln(os.Stderr, "2026-01-01T00:00:00Z ERR Error opening metrics server listener")
				os.Exit(4)
			}
			http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/ready" {
					http.NotFound(w, r)
					return
				}
				fmt.Fprintf(w, `{"status":200,"readyConnections":1,"connectorId":%q}`, id)
			}))
			os.Exit(4)
		}
	}
	os.Exit(m.Run())
}

func fixture(t *testing.T, name, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func dialConfig(t *testing.T) config {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	port := 0
	return config{Role: "dial", Binary: binary, ForwardPort: &port, Profile: "lab", Profiles: map[string]profile{
		"lab": {Hostname: "lab.example.com", ClientIDFile: fixture(t, "id", "fixture-id"), ClientSecretFile: fixture(t, "key", "fixture-secret")},
	}}
}

func waitState(t *testing.T, c *control, match func(snapshot) bool) snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	state := c.Status()
	for !match(state) {
		next, err := c.WaitForChange(ctx, state.Cursor)
		if err != nil {
			t.Fatalf("state %+v: %v", state, err)
		}
		state = next
	}
	return state
}

func reservePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", loopback(0))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestForwardLifecycle(t *testing.T) {
	t.Setenv("TUNNEL_TOKEN", "must-not-inherit")
	cfg := dialConfig(t)
	cfg.Profiles["next"] = cfg.Profiles["lab"]
	missingID, emptySecret := cfg.Profiles["lab"], cfg.Profiles["lab"]
	missingID.ClientIDFile = filepath.Join(t.TempDir(), "missing")
	emptySecret.ClientSecretFile = fixture(t, "key", "")
	cfg.Profiles["missing-id"], cfg.Profiles["empty-secret"] = missingID, emptySecret
	c := newControl(t.Context(), cfg, nil)
	t.Cleanup(func() { c.Stop() })
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	s := waitState(t, c, func(s snapshot) bool { return s.Phase == "listening" })
	conn, err := net.Dial("tcp", loopback(s.BoundPort))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte{0, 1, 0, 255}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "\x00\x01\x00\xff" {
		t.Fatalf("echo %x: %v", buf, err)
	}
	if err := c.Configure(map[string]any{"hostname": "other.example.com"}); err == nil {
		t.Fatal("accepted an arbitrary replacement target")
	}
	if err := c.Configure(map[string]any{"profile": "lab"}); err != nil || c.Status().BoundPort != s.BoundPort {
		t.Fatal("identical configuration disturbed the forwarder")
	}
	for _, name := range []string{"missing-id", "empty-secret"} {
		if err := c.Configure(map[string]any{"profile": name}); err == nil || err.Error() != "invalid-credentials" {
			t.Fatalf("switch to %s: %v", name, err)
		}
		if current := c.Status(); current.Profile != "lab" || current.BoundPort != s.BoundPort {
			t.Fatalf("failed switch changed active profile: %+v", current)
		}
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
			t.Fatalf("existing stream after rejected switch: %q, %v", buf, err)
		}
	}
	if err := c.Configure(map[string]any{"profile": "next"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, func(s snapshot) bool { return s.Profile == "next" && s.Phase == "listening" })
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("old stream survived profile switch")
	}
	// A rotated secret is read for each new child. The fixture rejects this one.
	if err := os.WriteFile(cfg.Profiles["next"].ClientSecretFile, []byte("revoked"), 0600); err != nil {
		t.Fatal(err)
	}
	conn2, err := net.Dial("tcp", loopback(c.Status().BoundPort))
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	conn2.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn2.Read(buf); err != io.EOF {
		t.Fatalf("denial should close the stream: %v", err)
	}
	waitState(t, c, func(s snapshot) bool { return s.LastError == "stream-ended" })
	var workers sync.WaitGroup
	for range 6 {
		workers.Add(1)
		go func() { defer workers.Done(); c.Stop(); c.Start() }()
	}
	workers.Wait()
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := c.Configure(map[string]any{"profile": "lab"}); err != nil || c.Status().Phase != "stopped" {
		t.Fatal("changing a stopped row started it")
	}
}

func TestBindCollisionAndConnectorRetry(t *testing.T) {
	cfg := dialConfig(t)
	l, err := net.Listen("tcp", loopback(0))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	cfg.ForwardPort = &port
	c := newControl(t.Context(), cfg, nil)
	c.Start()
	waitState(t, c, func(s snapshot) bool { return s.Phase == "failed" && s.LastError == "address-in-use" })
	c.Stop()
	t.Setenv("TUNNEL_SERVICE_TOKEN_SECRET", "must-not-inherit")
	token := filepath.Join(t.TempDir(), "tunnel")
	metrics := reservePort(t)
	connector := newControl(t.Context(), config{Role: "connector", Binary: cfg.Binary, MetricsPort: &metrics, TunnelTokenFile: token}, nil)
	t.Cleanup(func() { connector.Stop() })
	connector.Start()
	waitState(t, connector, func(s snapshot) bool {
		return s.Phase == "retrying" && s.Process == "stopped" && s.LastError == "invalid-credentials" && s.RetryAt != ""
	})
	if err := os.WriteFile(token, []byte("retry"), 0600); err != nil {
		t.Fatal(err)
	}
	waitState(t, connector, func(s snapshot) bool { return s.Phase == "retrying" && s.LastError == "connector-exited" })
	if err := os.WriteFile(token, []byte("fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	waitState(t, connector, func(s snapshot) bool {
		return s.EdgeReady != nil && *s.EdgeReady && s.LastError == "" && s.RetryAt == ""
	})
	connector.Stop()
	if s := connector.Status(); s.Phase != "stopped" || s.EdgeReady != nil {
		t.Fatalf("%+v", s)
	}
	if conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(metrics)); err == nil {
		conn.Close()
		t.Fatal("connector not reaped")
	}
}

func TestConnectorIgnoresHostConfig(t *testing.T) {
	binary, err := exec.LookPath("cloudflared")
	if err != nil {
		t.Skip("requires cloudflared CLI")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".cloudflared")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("token: invalid-fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Both credential sources fail locally, each with a distinguishable error.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	token := filepath.Join(home, "row-token-not-mounted")
	out, err := connectorCommand(ctx, binary, token, 0).CombinedOutput()
	if err == nil || !strings.Contains(string(out), filepath.Base(token)) {
		t.Fatalf("expected CLI to read the row token file: %v %s", err, out)
	}
}

func TestConnectorMetricsCollision(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	port := reservePort(t)
	cfg := config{Role: "connector", Binary: binary, MetricsPort: &port, TunnelTokenFile: fixture(t, "tunnel", "fixture-token")}
	first, second := newControl(t.Context(), cfg, nil), newControl(t.Context(), cfg, nil)
	t.Cleanup(func() { first.Stop(); second.Stop() })
	ready := func(s snapshot) bool { return s.EdgeReady != nil && *s.EdgeReady }
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, first, ready)
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, second, func(s snapshot) bool {
		if s.EdgeReady != nil {
			t.Fatalf("second connector reported another connector's readiness: %+v", s)
		}
		return s.LastError == "metrics-bind-failed" && s.RetryAt != ""
	})
	if err := first.Stop(); err != nil {
		t.Fatal(err)
	}
	waitState(t, second, ready)
}

func TestEdgeReadinessChecksConnectorID(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       *bool
	}{
		{"connected", `{"connectorId":"current"}`, 200, &yes},
		{"disconnected", `{"connectorId":"current"}`, 503, &no},
		{"foreign-ready", `{"connectorId":"other"}`, 200, nil},
		{"foreign-unavailable", `{"connectorId":"other"}`, 503, nil},
		{"unrecognized", "ready", 200, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			port := server.Listener.Addr().(*net.TCPAddr).Port
			if got := edgeReadiness(t.Context(), port, "current"); got != tc.want {
				t.Fatalf("readiness %v, want %v", got, tc.want)
			}
		})
	}
}
