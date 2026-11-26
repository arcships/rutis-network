//go:build rutishost

package libp2p

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	p2p "github.com/libp2p/go-libp2p"
	circuit "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
)

// RUTIS_HOST may be used to select the rutis-host 0.23 executable.
func TestTwoHosts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("rutis-host call needs a Unix control socket")
	}
	t.Run("direct", func(t *testing.T) { testTwoHosts(t, false) })
	t.Run("relay", func(t *testing.T) { testTwoHosts(t, true) })
}

func testTwoHosts(t *testing.T, useRelay bool) {
	host := os.Getenv("RUTIS_HOST")
	if host == "" {
		host = "rutis-host"
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "go")
	for name, src := range map[string]string{"link-libp2p": "./cmd/link-libp2p", "host-probe": "./testdata/host-probe"} {
		if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, name), src).CombinedOutput(); err != nil {
			t.Fatalf("build: %v: %s", err, out)
		}
	}
	type object = map[string]any
	isolate := object{"office-link": "local"}
	boot := func(name string, cfg config, extra object) string {
		t.Helper()
		home := filepath.Join(dir, name)
		if err := os.Mkdir(home, 0700); err != nil {
			t.Fatal(err)
		}
		cfg.ControlService = "office-link"
		wire := object{"id": "wire", "name": "go:link-libp2p", "config": cfg, "isolate": isolate}
		data, err := json.Marshal(object{"id": name, "runtimes": object{"go": object{"dir": bin}}, "rows": []object{wire, extra}})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, "rutis.json")
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		log, err := os.Create(filepath.Join(home, "host.log"))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(host, "run", path)
		cmd.Stdout, cmd.Stderr = log, log
		if err = cmd.Start(); err != nil {
			log.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Signal(os.Interrupt)
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			select {
			case <-exited:
			case <-time.After(8 * time.Second):
				cmd.Process.Kill()
				<-exited
				t.Errorf("%s timed out during shutdown", name)
			}
			log.Close()
			if t.Failed() {
				out, _ := os.ReadFile(log.Name())
				t.Logf("%s: %s", name, out)
			}
		})
		return path
	}
	call := func(path, method string, args ...any) (string, error) {
		t.Helper()
		argv := []string{"call", "--config", path, method}
		for _, v := range args {
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			argv = append(argv, string(b))
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, host, argv...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	wait := func(path, method string, wanted bool, args ...any) string {
		t.Helper()
		var out string
		var err error
		for end := time.Now().Add(12 * time.Second); time.Now().Before(end); {
			out, err = call(path, method, args...)
			if wanted && err == nil || !wanted && err != nil && strings.Contains(out, "no row provides") {
				return out
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%s: %v: %s", method, err, out)
		return ""
	}
	alpha, aID := identity(t)
	bravo, bID := identity(t)
	bConfig := config{KeyFile: bravo, Listen: "/ip4/127.0.0.1/udp/0/quic-v1", AllowPeer: aID.String()}
	if useRelay {
		h, err := p2p.New(p2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { h.Close() })
		r, err := circuit.New(h)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close() })
		bConfig.Relay = h.Addrs()[0].String() + "/p2p/" + h.ID().String()
	}
	observer := object{"id": "observer", "name": "go:link-test-probe", "config": object{"action": "witness", "relay": useRelay}, "isolate": isolate}
	b := boot("bravo", bConfig, observer)
	var address string
	if err := json.Unmarshal([]byte(wait(b, "witness.address", true)), &address); err != nil {
		t.Fatal(err)
	}
	group := object{"id": "local-ops", "group": true, "isolate": isolate, "config": []object{}}
	a := boot("alpha", config{KeyFile: alpha, Dial: address + "/p2p/" + bID.String()}, group)
	echo := func() {
		t.Helper()
		if got := wait(a, "witness.echo", true, "through the link"); got != `"through the link"` {
			t.Fatalf("echo: %s", got)
		}
	}
	echo()
	wait(a, "office-link.status", false)
	wait(b, "office-link.status", false)
	action := func(name string) {
		t.Helper()
		row := object{"id": "action-" + name, "name": "go:link-test-probe", "config": object{"action": name}}
		if out, err := call(a, "rutis/loader.create", row, "local-ops"); err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
	}
	action("stop")
	wait(a, "witness.echo", false, "stopped")
	action("start")
	echo()

	if !useRelay {
		static := config{KeyFile: alpha, Listen: "/ip4/127.0.0.1/udp/0/quic-v1", AllowPeer: bID.String()}
		create := func(cfg config, parent string) (string, error) {
			row := object{"id": "static", "name": "go:link-libp2p", "config": cfg}
			if parent == "" {
				return call(a, "rutis/loader.create", row)
			}
			return call(a, "rutis/loader.create", row, parent)
		}
		static.ControlService = "other-link"
		for _, parent := range []string{"", "local-ops"} {
			if out, err := create(static, parent); err == nil || !strings.Contains(out, "requires a matching row isolate") {
				t.Fatalf("controlService without matching isolate: %v, %s", err, out)
			}
		}
		// The host forwards inherited labels for declared services. This
		// provider picks its service name dynamically, so it needs its own label.
		static.ControlService = "office-link"
		if out, err := create(static, "local-ops"); err == nil || !strings.Contains(out, "requires a matching row isolate") {
			t.Fatalf("controlService with group-only isolation: %v, %s", err, out)
		}
		static.ControlService = ""
		occupied, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer occupied.Close()
		static.Listen = fmt.Sprintf("/ip4/127.0.0.1/udp/%d/quic-v1", occupied.LocalAddr().(*net.UDPAddr).Port)
		if out, err := create(static, ""); err == nil || !strings.Contains(out, "address already in use") {
			t.Fatalf("occupied listen address: %v, %s", err, out)
		}
		occupied.Close()
		if out, err := create(static, ""); err != nil {
			t.Fatalf("retry after releasing listen address: %v, %s", err, out)
		}
	}
}
