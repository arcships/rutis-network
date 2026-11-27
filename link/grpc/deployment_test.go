package grpclink

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Run the checked-in manifests with temporary paths, ports and certificates.
func TestCloudDeploymentExample(t *testing.T) {
	host := os.Getenv("RUTIS_HOST")
	if testing.Short() || host == "" || runtime.GOOS == "windows" {
		t.Skip("release host integration")
	}
	dir := t.TempDir()
	issue := testIssuer(t)
	replacements := []string{"/var/lib/rutis/grpc/routes.db", filepath.Join(dir, "routes.db")}
	for _, name := range []string{"directory", "east", "west", "east-agent", "west-agent", "edge", "worker"} {
		usage := x509.ExtKeyUsageClientAuth
		if name == "directory" || name == "east" || name == "west" {
			usage = x509.ExtKeyUsageServerAuth
		}
		id := issue(name+".test", usage)
		replacements = append(replacements, "/etc/rutis/grpc/"+name+".pem", id.CertFile, "/etc/rutis/grpc/"+name+".key", id.KeyFile)
		if name == "directory" {
			replacements = append(replacements, "/etc/rutis/grpc/ca.pem", id.CAFile)
		}
	}
	ports := map[string]net.Listener{}
	for name, addr := range map[string]string{"control": "127.0.0.1:7443", "east": "127.0.0.1:7444", "west": "127.0.0.1:7445"} {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		ports[name] = l
		replacements = append(replacements, addr, l.Addr().String())
	}
	paths := strings.NewReplacer(replacements...)
	config := func(file string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("example", "relay", file))
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(dir, file)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte(paths.Replace(string(b))), 0600); err != nil {
			t.Fatal(err)
		}
		return dest
	}
	for _, b := range []struct{ src, dst string }{{"./cmd/grpc-cloud", "grpc-cloud"}, {"./cmd/link-grpc", "edge/plugins/go/link-grpc"}, {"./example/device", "edge/plugins/go/device"}, {"./example/cloud", "worker/plugins/go/cloud-service"}} {
		if out, err := exec.Command("go", "build", "-o", filepath.Join(dir, b.dst), b.src).CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
	}
	if err := os.Link(filepath.Join(dir, "edge/plugins/go/link-grpc"), filepath.Join(dir, "worker/plugins/go/link-grpc")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"control", "east", "west"} {
		path := config(name + ".json")
		ports[name].Close()
		cli := exec.Command(filepath.Join(dir, "grpc-cloud"), path)
		log, err := os.CreateTemp(dir, name+"-*.log")
		if err != nil {
			t.Fatal(err)
		}
		cli.Stdout = log
		cli.Stderr = log
		if err := cli.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cli.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- cli.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("%s: %v", name, err)
				}
			case <-time.After(8 * time.Second):
				cli.Process.Kill()
				<-done
				t.Errorf("%s shutdown timed out", name)
			}
			log.Close()
			if t.Failed() {
				b, _ := os.ReadFile(log.Name())
				t.Log(string(b))
			}
		})
	}
	edge := config("edge/rutis.json")
	stopWorker := startHost(t, host, dir, config("worker/rutis.json"))
	startHost(t, host, dir, edge)
	var output []byte
	for end := time.Now().Add(40 * time.Second); time.Now().Before(end); {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		var err error
		output, err = exec.CommandContext(ctx, host, "call", "--config", edge, "device.submit", `"test"`).CombinedOutput()
		cancel()
		if err == nil {
			var r struct{ ID, Progress string }
			if json.Unmarshal(output, &r) != nil || r.ID != "job-1" || r.Progress != "job-1 queued" {
				t.Fatalf("unexpected receipt %s", output)
			}
			// Retire the cloud worker while the edge is still serving its local
			// control socket. The edge must withdraw the remotely mounted job row.
			stopWorker()
			until(t, func() bool {
				ctx, done := context.WithTimeout(t.Context(), 2*time.Second)
				defer done()
				b, err := exec.CommandContext(ctx, host, "call", "--config", edge, "device.submit", `"offline"`).CombinedOutput()
				return err != nil && strings.Contains(string(b), "no row provides")
			})
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("remote worker did not reply: %s", output)
}
