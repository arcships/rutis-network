package grpclink

import (
	"context"
	"encoding/json"
	"github.com/arcships/rutis-network/link/grpc/wire"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRegionalFailoverWithHosts(t *testing.T) {
	if testing.Short() {
		t.Skip("rutis-host integration")
	}
	host := os.Getenv("RUTIS_HOST")
	if host == "" || runtime.GOOS == "windows" {
		t.Skip("requires RUTIS_HOST and Unix sockets")
	}
	x := cloudFixture(t, 5*time.Second)
	dir := t.TempDir()
	build := func(site, name, src string) {
		t.Helper()
		dst := filepath.Join(dir, site, "plugins", "go", name)
		if out, err := exec.Command("go", "build", "-o", dst, src).CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", name, err, out)
		}
	}
	build("edge", "link-grpc", "./cmd/link-grpc")
	build("edge", "device", "./example/device")
	build("cloud", "worker", "./test/worker")
	dst := filepath.Join(dir, "cloud", "plugins", "go", "link-grpc")
	if err := os.Link(filepath.Join(dir, "edge", "plugins", "go", "link-grpc"), dst); err != nil {
		t.Fatal(err)
	}
	type row = map[string]any
	wireRow := func(id Identity, target string) row {
		return row{"id": "wire", "name": "go:link-grpc", "config": ClientConfig{Identity: id, Address: x.address, ServerName: "cloud.test", Cloud: &CloudConfig{Pair: "jobs", Target: target, Region: "east", Capabilities: []string{"rpc"}, Load: 12}}}
	}
	receipts := filepath.Join(dir, "receipts")
	cloud := writeHostManifest(t, dir, "cloud", wireRow(x.b, "mobile"), row{"id": "jobs", "name": "go:worker", "config": row{"receiptFile": receipts}})
	edge := writeHostManifest(t, dir, "edge", wireRow(x.a, "worker"), row{"id": "device", "name": "go:device"}, row{"id": "remote", "name": "wire:go:remote-marker"})
	startHost(t, host, dir, cloud)
	startHost(t, host, dir, edge)
	call := func(method string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		cmd := append([]string{"call", "--config", edge, method}, args...)
		b, err := exec.CommandContext(ctx, host, cmd...).CombinedOutput()
		return strings.TrimSpace(string(b)), err
	}
	// The marker binary is installed only on the cloud host.
	until(t, func() bool { v, err := call("marker.pid"); return err == nil && v != "" })
	submit := func(want string) {
		t.Helper()
		var v string
		var err error
		until(t, func() bool {
			v, err = call("device.submit", `"build"`)
			return err == nil || !strings.Contains(v, "no row provides")
		})
		var receipt struct{ ID, Progress string }
		if err != nil || json.Unmarshal([]byte(v), &receipt) != nil || receipt.ID != want || receipt.Progress != want+" queued" {
			t.Fatalf("job callback: %s %v", v, err)
		}
	}
	submit("job-1")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	hold := exec.CommandContext(ctx, host, "call", "--config", edge, "jobs.hold")
	done := make(chan error, 1)
	go func() { done <- hold.Run() }()
	until(t, func() bool { b, _ := os.ReadFile(receipts); return string(b) == "accepted\n" })
	// Only the directory heartbeat fails. Both regional data sockets stay live
	// until the old relay fences itself. Holding west offline makes withdrawal observable.
	x.regions["west"].blocked.Store(true)
	until(t, func() bool { _, err := x.regions["west"].Ping(t.Context(), &wire.Probe{}); return err != nil })
	x.regions["east"].blocked.Store(true)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("interrupted call succeeded")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("in-flight call did not settle")
	}
	until(t, func() bool {
		v, err := call("device.submit", `"offline"`)
		return err != nil && strings.Contains(v, "no row provides")
	})
	x.regions["west"].blocked.Store(false)
	until(t, func() bool { v, err := call("marker.pid"); return err == nil && v != "" })
	submit("job-2")
	until(t, func() bool {
		s, err := x.directory.Inspect(t.Context(), &wire.InspectRequest{})
		if err != nil || len(s.Hosts) != 2 {
			return false
		}
		for _, r := range s.Regions {
			if r.Report.Region == "west" && r.Available && r.Report.Streams == 2 && r.Report.Messages > 0 {
				return true
			}
		}
		return false
	})
	if b, err := os.ReadFile(receipts); err != nil || string(b) != "accepted\n" {
		t.Fatalf("in-flight job replayed: %q %v", b, err)
	}
}
