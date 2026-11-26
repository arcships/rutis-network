//go:build rutishost

package tailscale

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"
)

type object = map[string]any

func TestHosts(t *testing.T) {
	host := os.Getenv("RUTIS_HOST")
	if host == "" {
		host = "rutis-host"
	}
	bin := filepath.Join(t.TempDir(), "go")
	for name, source := range map[string]string{"link-tailscale": "./cmd/link-tailscale", "tailnet-probe": "./testdata/probe"} {
		if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, name), source).CombinedOutput(); err != nil {
			t.Fatalf("build: %v: %s", err, out)
		}
	}
	t.Run("tsnet", func(t *testing.T) {
		s := &testcontrol.Server{DERPMap: integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1"), DNSConfig: &tailcfg.DNSConfig{Proxied: true}, MagicDNSDomain: "test.ts.net"}
		s.HTTPTestServer = httptest.NewServer(s)
		t.Cleanup(s.HTTPTestServer.Close)
		t.Setenv("TS_CONTROL_URL", s.HTTPTestServer.URL)
		t.Setenv("TS_AUTHKEY", "test-only")
		t.Setenv("TS_NO_LOGS_NO_SUPPORT", "true")
		exerciseHosts(t, host, bin, "alpha", "bravo", true)
	})
	t.Run("system", func(t *testing.T) {
		if os.Getenv("RUTIS_TAILSCALE_SYSTEM_TEST") != "1" {
			t.Skip("opt in on an enrolled local node")
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		s, err := new(local.Client).Status(ctx)
		if err != nil || s.BackendState != "Running" || len(s.TailscaleIPs) == 0 {
			t.Fatalf("local tailscaled unavailable: %v", err)
		}
		ip := s.TailscaleIPs[0].String()
		exerciseHosts(t, host, bin, ip, ip, false)
	})
}

func exerciseHosts(t *testing.T, host, bin, aPeer, bPeer string, embedded bool) {
	t.Helper()
	start := func(id string, rows []object) (path, logfile string) {
		t.Helper()
		dir := t.TempDir()
		path = filepath.Join(dir, "rutis.json")
		config, err := json.Marshal(object{"id": id, "runtimes": object{"go": object{"dir": bin}}, "rows": rows})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, config, 0600); err != nil {
			t.Fatal(err)
		}
		logfile = filepath.Join(dir, "host.log")
		log, err := os.Create(logfile)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(host, "run", path)
		cmd.Stdout, cmd.Stderr = log, log
		if err = cmd.Start(); err != nil {
			log.Close()
			t.Fatal(err)
		}
		log.Close()
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		t.Cleanup(func() {
			cmd.Process.Signal(os.Interrupt)
			select {
			case err := <-exited:
				if err != nil {
					t.Errorf("%s exit: %v", id, err)
				}
			case <-time.After(15 * time.Second):
				cmd.Process.Kill()
				<-exited
				t.Errorf("%s shutdown timed out", id)
			}
			if t.Failed() {
				data, _ := os.ReadFile(logfile)
				t.Logf("%s: %s", id, data)
			}
		})
		return path, logfile
	}
	call := func(path, method string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, host, append([]string{"call", "--config", path, method}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	wire := func(cfg object) object { return object{"id": "wire", "name": "go:link-tailscale", "config": cfg} }
	listen := object{"listen": ":0", "allowPeer": aPeer}
	dial := object{}
	if embedded {
		listen["tsnet"] = object{"stateDir": t.TempDir(), "hostname": "bravo"}
		dial["tsnet"] = object{"stateDir": t.TempDir(), "hostname": "alpha"}
	}
	b, log := start("bravo", []object{wire(listen), {"id": "echo", "name": "go:tailnet-probe"}})
	var port string
	bound := regexp.MustCompile(`link/tailscale listening [^\s]+:(\d+)`)
	for deadline := time.Now().Add(40 * time.Second); time.Now().Before(deadline); {
		data, _ := os.ReadFile(log)
		if m := bound.FindSubmatch(data); m != nil {
			port = string(m[1])
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if port == "" {
		t.Fatal("listener did not publish its port")
	}
	dial["dial"] = net.JoinHostPort(bPeer, port)
	a, _ := start("alpha", []object{wire(dial)})
	waitEcho := func(want bool) {
		t.Helper()
		var out string
		var err error
		for deadline := time.Now().Add(40 * time.Second); time.Now().Before(deadline); {
			out, err = call(a, "witness.echo", `"tailnet"`)
			if want && err == nil && out == `"tailnet"` || !want && err != nil && strings.Contains(out, "no row provides") {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("remote echo available=%t: %v %s", want, err, out)
	}
	waitEcho(true)
	remove := func(path string) {
		t.Helper()
		if out, err := call(path, "rutis/loader.remove", `"wire"`); err != nil {
			t.Fatalf("remove: %v %s", err, out)
		}
	}
	create := func(path string, cfg object) {
		t.Helper()
		row, err := json.Marshal(wire(cfg))
		if err != nil {
			t.Fatal(err)
		}
		if out, err := call(path, "rutis/loader.create", string(row)); err != nil {
			t.Fatalf("create: %v %s", err, out)
		}
	}
	remove(a)
	waitEcho(false)
	create(a, dial)
	waitEcho(true)
	// Keep the dialing row running while replacing the listener.
	remove(b)
	waitEcho(false)
	listen["listen"] = ":" + port
	listen["allowPeer"] = "unlisted.test.ts.net"
	create(b, listen)
	rejected := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		data, _ := os.ReadFile(log)
		if strings.Contains(string(data), "peer-not-allowed") {
			rejected = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !rejected {
		t.Fatal("listener did not report an unlisted peer")
	}
	waitEcho(false)
	remove(b)
	listen["allowPeer"] = aPeer
	create(b, listen)
	waitEcho(true)
}
