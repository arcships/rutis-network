package cloudflared

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcships/rutis/go/rutis/rutistest"
	"github.com/gorilla/websocket"
)

// Run with RUTIS_HOST pointing to a 0.23.0 executable. RUTIS_TCP_PLUGIN
// additionally exercises both dedicated TCP control services from #16.
func TestHostInterconnect(t *testing.T) {
	host := os.Getenv("RUTIS_HOST")
	if host == "" || runtime.GOOS == "windows" {
		t.Skip("requires RUTIS_HOST 0.23.0 and Unix host control sockets")
	}
	cloudflared, err := exec.LookPath("cloudflared")
	if err != nil {
		t.Fatal(err)
	}
	plugins := t.TempDir()
	for _, plugin := range []struct{ name, path string }{{"link-cloudflared", "./cmd/link-cloudflared"}, {"business", "./example/business"}} {
		if out, err := exec.Command("go", "build", "-o", filepath.Join(plugins, plugin.name), plugin.path).CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, out)
		}
	}
	tcp := os.Getenv("RUTIS_TCP_PLUGIN")
	if tcp != "" {
		if err := os.Symlink(tcp, filepath.Join(plugins, "link-tcp")); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("builtin", func(t *testing.T) { hostPair(t, host, cloudflared, plugins, false) })
	if tcp != "" {
		t.Run("managed-tcp", func(t *testing.T) { hostPair(t, host, cloudflared, plugins, true) })
	}
}

func hostPair(t *testing.T, host, cloudflared, plugins string, managed bool) {
	origin, local := reservePort(t), reservePort(t)
	for origin == local {
		local = reservePort(t)
	}
	metrics := reservePort(t)
	for metrics == origin || metrics == local {
		metrics = reservePort(t)
	}

	// Test edge validates service headers then forwards opaque bytes to the
	// real origin listener. No request leaves loopback.
	var denied atomic.Int64
	upgrader := websocket.Upgrader{}
	edge := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cf-Access-Client-Id") != "fixture-id" || r.Header.Get("Cf-Access-Client-Secret") != "fixture-secret" {
			denied.Add(1)
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		wire, err := net.DialTimeout("tcp", loopback(origin), time.Second)
		if err != nil {
			return
		}
		defer wire.Close()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer ws.Close()
			buf := make([]byte, 32768)
			for {
				n, err := wire.Read(buf)
				if n > 0 && ws.WriteMessage(websocket.BinaryMessage, buf[:n]) != nil {
					return
				}
				if err != nil {
					return
				}
			}
		}()
		for {
			_, reader, err := ws.NextReader()
			if err != nil {
				break
			}
			if _, err = io.Copy(wire, reader); err != nil {
				break
			}
		}
		wire.Close()
		<-done
	}))
	t.Cleanup(edge.Close)
	edgePort := edge.Listener.Addr().(*net.TCPAddr).Port
	// cloudflared's own local test flag routes just this fixture to our TLS server.
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := fmt.Sprintf("#!/bin/sh\nexec %s \"$@\" --connect-to localhost:%d:127.0.0.1\n", quote(cloudflared), edgePort)
	binary := fixture(t, "access", script)
	if err := os.Chmod(binary, 0700); err != nil {
		t.Fatal(err)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	secret := fixture(t, "access-secret", "revoked")
	type object = map[string]any
	row := func(id, name string, cfg object) object { return object{"id": id, "name": name, "config": cfg} }
	aCF := object{"binary": binary, "role": "dial", "forwardPort": local, "controlService": "dial-control", "profile": "lab", "profiles": object{"lab": object{
		"hostname": "lab.example.com", "servername": "localhost", "clientIdFile": fixture(t, "id", "fixture-id"), "clientSecretFile": secret,
	}}}
	bCF := object{"binary": testBinary, "role": "connector", "tunnelTokenFile": fixture(t, "tunnel", "fixture-token"), "metricsPort": metrics, "controlService": "origin-control"}
	aTunnel, bTunnel := row("access", "go:link-cloudflared", aCF), row("tunnel", "go:link-cloudflared", bCF)
	aTunnel["isolate"], bTunnel["isolate"] = object{"dial-control": "management"}, object{"origin-control": "management"}
	a := object{"id": "laptop", "runtimes": object{"go": object{"dir": plugins}}}
	b := object{"id": "office", "runtimes": a["runtimes"]}
	var dial, listen object
	if managed {
		certDir := t.TempDir()
		cert, key := filepath.Join(certDir, "cert.pem"), filepath.Join(certDir, "key.pem")
		conf := fixture(t, "openssl.cnf", "[req]\nprompt=no\ndistinguished_name=dn\nx509_extensions=ext\n[dn]\nCN=localhost\n[ext]\nbasicConstraints=critical,CA:TRUE\nsubjectAltName=DNS:localhost\n")
		if out, err := exec.Command("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-config", conf, "-keyout", key, "-out", cert).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		tcpRow := func(role string, parent, cfg object) object {
			name := role + "-tcp"
			cfg["tcpService"] = name
			parent["inject"] = []string{name}
			parent["isolate"].(object)[name] = "internal"
			wire := row("wire", "go:link-tcp-"+role, object{"controlService": name, "autoStart": false, "ca": cert, "cert": cert, "key": key})
			wire["isolate"] = object{name: "internal"}
			return wire
		}
		bCF["originPort"] = origin
		dial, listen = tcpRow("dial", aTunnel, aCF), tcpRow("listen", bTunnel, bCF)
	} else {
		b["listen"] = []object{{"name": "local", "address": loopback(origin)}}
		dial = row("office", "rutis-host/dial", object{"address": "ws://" + loopback(local) + "/rutis"})
		listen = row("laptop", "rutis-host/listen", object{"listener": "local"})
	}
	a["rows"] = []object{dial, aTunnel, row("consumer", "go:consumer", nil)}
	b["rows"] = []object{listen, bTunnel, row("greeter", "go:greeter", nil)}
	start := func(config object, token string) (string, string) {
		t.Helper()
		dir := t.TempDir()
		data, _ := json.Marshal(config)
		path := filepath.Join(dir, "rutis.json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		trace := filepath.Join(dir, "host.log")
		log, err := os.Create(trace)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(host, "run", "--watch-config", path)
		cmd.Env = append(os.Environ(), token+"=local-test-link-key")
		cmd.Dir = dir
		cmd.Stdout = log
		cmd.Stderr = log
		if err := cmd.Start(); err != nil {
			log.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cmd.Process.Signal(os.Interrupt)
			timer := time.AfterFunc(5*time.Second, func() { cmd.Process.Kill() })
			cmd.Wait()
			timer.Stop()
			log.Close()
		})
		return path, trace
	}
	_, blog := start(b, "RUTIS_TOKEN_LAPTOP")
	apath, alog := start(a, "RUTIS_TOKEN_OFFICE")
	traces := func() string { x, _ := os.ReadFile(alog); y, _ := os.ReadFile(blog); return string(x) + string(y) }
	call := func(method string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		argv := append([]string{"call", "--config", apath, method}, args...)
		out, err := exec.CommandContext(ctx, host, argv...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	for deadline := time.Now().Add(12 * time.Second); denied.Load() == 0; {
		if time.Now().After(deadline) {
			t.Fatalf("no request observed: %s", traces())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := call("consumer.greet", strconv.Quote("Rutis")); err == nil {
		t.Fatal("revoked credential reached service")
	}
	if err := os.WriteFile(secret, []byte("fixture-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	checkCall := func() {
		for deadline := time.Now().Add(20 * time.Second); ; {
			out, err := call("consumer.greet", strconv.Quote("Rutis"))
			if err == nil && out == strconv.Quote("office: hello Rutis") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("call %s: %v\n%s", out, err, traces())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	checkCall()
	if _, err := call("origin-control.status"); err == nil {
		t.Fatal("remote management service exported")
	}
	// Unmount only the Cloudflare row, leaving its TCP dependency in place.
	// Remounting must reacquire a stopped instance and create a new session.
	rows := a["rows"].([]object)
	reload := func(next []object) {
		a["rows"] = next
		data, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(apath+".new", data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(apath+".new", apath); err != nil {
			t.Fatal(err)
		}
	}
	reload([]object{rows[0], rows[2]})
	for deadline := time.Now().Add(10 * time.Second); ; {
		if _, err := call("consumer.greet", strconv.Quote("Rutis")); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("old session survived unload: %s", traces())
		}
		time.Sleep(50 * time.Millisecond)
	}
	reload(rows)
	checkCall()
	if strings.Contains(traces(), "fixture-secret") {
		t.Fatal("credential leaked into host log")
	}
}

func TestControlRPC(t *testing.T) {
	if os.Getenv("RUTIS_HOST") == "" {
		t.Skip("requires RUTIS_HOST")
	}
	cfg := dialConfig(t)
	loaded := rutistest.Load(t, Plugin, map[string]any{
		"role": "dial", "controlService": "control", "binary": cfg.Binary, "forwardPort": 0,
		"autoStart": false, "profile": "lab", "profiles": map[string]any{"lab": cfg.Profiles["lab"], "backup": cfg.Profiles["lab"]},
	}, nil)
	service := loaded.Service("control")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var state snapshot
	invoke := func(name string, result any, args ...any) {
		t.Helper()
		if err := service.Call(ctx, name, result, args...); err != nil {
			t.Fatal(err)
		}
	}
	invoke("status", &state)
	if state.Phase != "idle" || state.Cursor == "" {
		t.Fatalf("%+v", state)
	}
	invoke("configure", nil, map[string]any{"profile": "backup"})
	invoke("start", nil)
	for state.Phase != "listening" {
		invoke("waitForChange", &state, state.Cursor)
	}
	if state.Profile != "backup" || state.BoundPort == 0 {
		t.Fatalf("%+v", state)
	}
	invoke("stop", nil)
	invoke("status", &state)
	if state.Phase != "stopped" {
		t.Fatalf("%+v", state)
	}
}
