package grpclink

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/arcships/rutis-network/link/grpc/wire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func testIdentities(t *testing.T) (Identity, Identity, Identity) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, kind string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: data}), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	key := func() *ecdsa.PrivateKey {
		k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		return k
	}
	caKey := key()
	template := func(n int64) *x509.Certificate {
		return &x509.Certificate{SerialNumber: big.NewInt(n), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	}
	ca := template(1)
	ca.Subject = pkix.Name{CommonName: "test-root"}
	ca.IsCA = true
	ca.BasicConstraintsValid = true
	ca.KeyUsage = x509.KeyUsageCertSign
	cert := func(c *x509.Certificate, k *ecdsa.PrivateKey) []byte {
		b, e := x509.CreateCertificate(rand.Reader, c, ca, k.Public(), caKey)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	root := write("ca.pem", "CERTIFICATE", cert(ca, caKey))
	issue := func(name string, n int64, usage x509.ExtKeyUsage) Identity {
		c := template(n)
		c.DNSNames = []string{name}
		c.ExtKeyUsage = []x509.ExtKeyUsage{usage}
		c.KeyUsage = x509.KeyUsageDigitalSignature
		k := key()
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		return Identity{CAFile: root, CertFile: write(name+".crt", "CERTIFICATE", cert(c, k)), KeyFile: write(name+".key", "EC PRIVATE KEY", der)}
	}
	return issue("cloud.test", 2, x509.ExtKeyUsageServerAuth), issue("device.test", 3, x509.ExtKeyUsageClientAuth), issue("other.test", 4, x509.ExtKeyUsageClientAuth)
}

func testConn(t *testing.T, address string, id Identity) *grpc.ClientConn {
	t.Helper()
	cfg, err := id.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ServerName = "cloud.test"
	cc, err := grpc.NewClient("passthrough:///"+address, grpc.WithTransportCredentials(credentials.NewTLS(cfg)), grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(MaxWireSize), grpc.MaxCallRecvMsgSize(MaxWireSize)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return cc
}

func TestEmbeddedCloudService(t *testing.T) {
	if testing.Short() {
		t.Skip("rutis-host integration")
	}
	host := os.Getenv("RUTIS_HOST")
	if host == "" || runtime.GOOS == "windows" {
		t.Skip("requires RUTIS_HOST 0.23 and Unix control sockets")
	}
	dir := t.TempDir()
	for _, binary := range []struct{ project, name, src string }{
		{"cloud", "cloud-service", "./example/cloud"},
		{"device", "link-grpc", "./cmd/link-grpc"},
		{"device", "device", "./example/device"},
	} {
		path := filepath.Join(dir, binary.project, "plugins", "go", binary.name)
		if out, err := exec.Command("go", "build", "-o", path, binary.src).CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", binary.name, err, out)
		}
	}
	server, client, other := testIdentities(t)
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	type obj = map[string]any
	exampleRows := func(name, addressField string, id Identity) []obj {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("example", name, "rutis.json"))
		if err != nil {
			t.Fatal(err)
		}
		var project struct{ Rows []obj }
		if err := json.Unmarshal(data, &project); err != nil {
			t.Fatal(err)
		}
		cfg := project.Rows[0]["config"].(obj)
		cfg[addressField] = addr
		cfg["caFile"], cfg["certFile"], cfg["keyFile"] = id.CAFile, id.CertFile, id.KeyFile
		return project.Rows
	}
	cloudRows := exampleRows("cloud", "listen", server)
	app, embedded := cloudRows[0], cloudRows[1]
	manifest := func(id string, rows ...obj) string {
		home := filepath.Join(dir, id)
		if err := os.MkdirAll(home, 0700); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(obj{"id": id, "runtimes": obj{"go": obj{"dir": "./plugins/go"}}, "rows": rows})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, "rutis.json")
		if err := os.WriteFile(path+".new", data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".new", path); err != nil {
			t.Fatal(err)
		}
		return path
	}
	start := func(path string) func() {
		log, err := os.CreateTemp(dir, "host-*.log")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(host, "run", "--watch-config", path)
		cmd.Dir = dir
		cmd.Stdout = log
		cmd.Stderr = log
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			cmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				cmd.Process.Kill()
				<-done
				t.Error("host shutdown timed out")
			}
			log.Close()
		}
		t.Cleanup(func() {
			stop()
			if t.Failed() {
				b, _ := os.ReadFile(log.Name())
				t.Log(string(b))
			}
		})
		return stop
	}
	cloudPath := manifest("cloud", app, embedded)
	stopCloud := start(cloudPath)
	devicePath := manifest("device", exampleRows("device", "address", client)...)
	start(devicePath)
	call := func() (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		b, err := exec.CommandContext(ctx, host, "call", "--config", devicePath, "device.submit", `"build"`).CombinedOutput()
		return strings.TrimSpace(string(b)), err
	}
	wait := func(available bool) string {
		t.Helper()
		var out string
		var err error
		for until := time.Now().Add(15 * time.Second); time.Now().Before(until); {
			out, err = call()
			if available && err == nil {
				var r struct{ ID, Progress string }
				if err := json.Unmarshal([]byte(out), &r); err != nil || r.ID == "" || r.Progress != r.ID+" queued" {
					t.Fatalf("missing reverse callback: %s", out)
				}
				return r.ID
			}
			if !available && err != nil && strings.Contains(out, "no row provides") {
				return ""
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("available=%v: %s (%v)", available, out, err)
		return ""
	}
	if id := wait(true); id != "job-1" {
		t.Fatalf("first job %s", id)
	}
	probe := testConn(t, addr, other)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	stream, err := wire.NewLinkClient(probe).Tunnel(ctx)
	if err == nil {
		_, err = stream.Recv()
	}
	cancel()
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign device: %v", err)
	}

	// Remove only the cloud plugin. The health RPC and in-process scheduler
	// survive, and the same device row reconnects when the plugin is rebound.
	manifest("cloud", app)
	wait(false)
	ctx, cancel = context.WithTimeout(t.Context(), 3*time.Second)
	health, err := healthpb.NewHealthClient(probe).Check(ctx, &healthpb.HealthCheckRequest{})
	cancel()
	if err != nil || health.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("application health: %v %v", health, err)
	}
	manifest("cloud", app, embedded)
	if id := wait(true); id == "job-1" {
		t.Fatal("detaching the plugin restarted the application")
	}

	stopCloud()
	wait(false)
	start(cloudPath)
	if id := wait(true); id != "job-1" {
		t.Fatalf("fresh cloud process returned %s", id)
	}
}
