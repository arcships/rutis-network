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
		t.Run("pinned", func(t *testing.T) { exerciseHosts(t, host, bin, "alpha", "bravo", true) })
		t.Run("discovery", func(t *testing.T) { exerciseDiscovery(t, host, bin, s) })
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

func startHost(t *testing.T, host, bin, id string, rows []object) (path, logfile string) {
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

func callHost(t *testing.T, host, path, method string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, host, append([]string{"call", "--config", path, method}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func wire(cfg object) object { return object{"id": "wire", "name": "go:link-tailscale", "config": cfg} }

func exerciseHosts(t *testing.T, host, bin, aPeer, bPeer string, embedded bool) {
	t.Helper()
	listen := object{"listen": ":0", "allowPeer": aPeer}
	dial := object{}
	if embedded {
		listen["tsnet"] = object{"stateDir": t.TempDir(), "hostname": "bravo"}
		dial["tsnet"] = object{"stateDir": t.TempDir(), "hostname": "alpha"}
	}
	b, log := startHost(t, host, bin, "bravo", []object{wire(listen), {"id": "echo", "name": "go:tailnet-probe"}})
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
	a, _ := startHost(t, host, bin, "alpha", []object{wire(dial)})
	waitEcho := func(want bool) { t.Helper(); waitRPC(t, host, a, "witness", want) }
	waitEcho(true)
	remove := func(path string) {
		t.Helper()
		if out, err := callHost(t, host, path, "rutis/loader.remove", `"wire"`); err != nil {
			t.Fatalf("remove: %v %s", err, out)
		}
	}
	create := func(path string, cfg object) {
		t.Helper()
		row, err := json.Marshal(wire(cfg))
		if err != nil {
			t.Fatal(err)
		}
		if out, err := callHost(t, host, path, "rutis/loader.create", string(row)); err != nil {
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

func waitRPC(t *testing.T, host, path, service string, want bool) {
	t.Helper()
	var out string
	var err error
	for deadline := time.Now().Add(40 * time.Second); time.Now().Before(deadline); {
		out, err = callHost(t, host, path, service+".echo", `"tailnet"`)
		if want && err == nil && out == `"tailnet"` || !want && err != nil && strings.Contains(out, "no row provides") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("remote %s available=%t: %v %s", service, want, err, out)
}

func exerciseDiscovery(t *testing.T, host, bin string, control *testcontrol.Server) {
	const workerTag, controllerTag = "tag:rutis-worker", "tag:rutis-controller"
	startLeaf := func(id, service string) string {
		t.Helper()
		_, log := startHost(t, host, bin, id, []object{
			wire(object{"allowTag": controllerTag, "tsnet": object{"hostname": id, "stateDir": t.TempDir()}}),
			{"id": "echo", "name": "go:tailnet-probe", "config": object{"service": service}},
		})
		return log
	}
	node := func(hostname string) *tailcfg.Node {
		t.Helper()
		for deadline := time.Now().Add(35 * time.Second); time.Now().Before(deadline); {
			for _, p := range control.AllNodes() {
				if p.Hostinfo.Hostname() == hostname {
					return p
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("node %s did not enroll", hostname)
		return nil
	}
	tag := func(hostname, tag string) {
		t.Helper()
		p := node(hostname)
		p.Tags = []string{tag}
		control.UpdateNode(p)
	}
	controller, _ := startHost(t, host, bin, "controller", []object{})
	logOne := startLeaf("worker-one", "one")
	logTwo := startLeaf("worker-two", "two")
	tag("worker-one", workerTag)
	tag("worker-two", workerTag)
	wait := func(service string, want bool) { t.Helper(); waitRPC(t, host, controller, service, want) }
	wait("one", false)
	discovery, err := json.Marshal(object{
		"id": "leaves", "name": "go:link-tailscale",
		"config": object{"discover": object{"tag": workerTag}, "tsnet": object{"hostname": "controller", "stateDir": t.TempDir()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	loader := func(method string, args ...string) string {
		t.Helper()
		out, err := callHost(t, host, controller, "rutis/loader."+method, args...)
		if err != nil {
			t.Fatalf("loader.%s: %v %s", method, err, out)
		}
		return out
	}
	id := loader("create", string(discovery))
	if id != `"leaves"` {
		t.Fatalf("create returned %s, want the configured row ID", id)
	}
	checkStatus := func(running bool) {
		t.Helper()
		var snapshot struct{ Rows []struct{ ID, State string } }
		if err := json.Unmarshal([]byte(loader("status")), &snapshot); err != nil {
			t.Fatal(err)
		}
		for _, row := range snapshot.Rows {
			if row.ID == "leaves" {
				if !running || row.State != "running" {
					t.Fatalf("unexpected discovery row %+v, want running=%t", row, running)
				}
				return
			}
		}
		if running {
			t.Fatal("discovery row missing from loader.status")
		}
	}
	// Discovery alone cannot admit a controller without an approved tag.
	for _, path := range []string{logOne, logTwo} {
		for deadline := time.Now().Add(40 * time.Second); ; {
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), "peer-not-allowed") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("untagged controller never reached admission")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	wait("one", false)
	checkStatus(true)
	tag("controller", controllerTag)
	wait("one", true)
	wait("two", true)
	loader("reload", id)
	wait("one", true)
	wait("two", true)
	checkStatus(true)
	connections := func() int {
		t.Helper()
		data, err := os.ReadFile(logOne)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(data), "link/tailscale connected ")
	}
	beforeRename := connections()
	worker := node("worker-one")
	worker.Name = "renamed.test.ts.net."
	control.UpdateNode(worker)
	tag("worker-two", "tag:retired")
	wait("two", false)
	wait("one", true)
	if connections() != beforeRename {
		t.Fatal("renaming the worker replaced its connection")
	}
	tag("worker-two", workerTag)
	wait("two", true)
	// Only the listeners see their inbound peer lose its required tag.
	tag("controller", "tag:retired")
	wait("one", false)
	wait("two", false)
	tag("controller", controllerTag)
	wait("one", true)
	wait("two", true)
	// Withdraw just one visible node from the controller's map.
	hostKey := node("controller").Key
	leaf := node("worker-two")
	if !control.AddRawMapResponse(hostKey, &tailcfg.MapResponse{PeersRemoved: []tailcfg.NodeID{leaf.ID}}) {
		t.Fatal("controller is disconnected from control")
	}
	wait("two", false)
	wait("one", true)
	if !control.AddRawMapResponse(hostKey, &tailcfg.MapResponse{PeersChanged: []*tailcfg.Node{leaf}}) {
		t.Fatal("could not restore worker in controller's map")
	}
	wait("two", true)
	loader("remove", id)
	wait("one", false)
	wait("two", false)
	checkStatus(false)
}
