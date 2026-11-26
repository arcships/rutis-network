package cloudflared

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
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
			if len(args) != 6 || args[0] != "--no-autoupdate" || args[1] != "--metrics" || args[3] != "run" || args[4] != "--token-file" ||
				os.Getenv("TUNNEL_TOKEN") != "" || os.Getenv("TUNNEL_SERVICE_TOKEN_SECRET") != "" {
				os.Exit(2)
			}
			data, _ := os.ReadFile(args[5])
			if string(data) != "fixture-token" {
				os.Exit(3)
			}
			http.ListenAndServe(args[2], http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/ready" {
					http.NotFound(w, r)
					return
				}
				fmt.Fprint(w, "ready")
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
	token := fixture(t, "tunnel", "retry")
	metrics := reservePort(t)
	connector := newControl(t.Context(), config{Role: "connector", Binary: cfg.Binary, MetricsPort: &metrics, TunnelTokenFile: token}, nil)
	t.Cleanup(func() { connector.Stop() })
	connector.Start()
	waitState(t, connector, func(s snapshot) bool { return s.Phase == "retrying" && s.RetryAt != "" })
	if err := os.WriteFile(token, []byte("fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	waitState(t, connector, func(s snapshot) bool { return s.EdgeReady != nil && *s.EdgeReady })
	connector.Stop()
	if s := connector.Status(); s.Phase != "stopped" || s.EdgeReady != nil {
		t.Fatalf("%+v", s)
	}
	if conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(metrics)); err == nil {
		conn.Close()
		t.Fatal("connector not reaped")
	}
}
