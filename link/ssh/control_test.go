package linkssh

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
)

func TestConfigureValidatesWholePatch(t *testing.T) {
	profiles := map[string]profile{"office": {}}
	initial := settings{endpoint: endpoint{SSHAddress: "bastion:22", TargetAddress: "[::1]:7443", Profile: "office"}}
	for _, patch := range []map[string]any{
		nil, {"reconnect": 1}, {"sshAddress": "no-port"}, {"targetAddress": "host:0"}, {"targetAddress": ":7443"},
		{"targetAddress": "other:7443", "profile": "missing"}, {"servername": "bad/name"}, {"knownHosts": "/tmp/key"},
	} {
		c := &control{row: t.Context(), profiles: profiles, state: snapshot{Config: initial}}
		if err := c.Configure(patch); err == nil {
			t.Fatalf("accepted %v", patch)
		}
		s, _ := c.Status()
		if s.Config != initial {
			t.Fatal("invalid patch mutated settings")
		}
	}
	next, err := initial.patch(map[string]any{"sshAddress": "[::1]:2222", "servername": "office.local", "reconnect": true}, dialRole, profiles)
	if err != nil || next.TargetAddress != initial.TargetAddress || !next.Reconnect {
		t.Fatalf("patch: %+v, %v", next, err)
	}
	next, err = next.patch(map[string]any{"servername": nil}, dialRole, profiles)
	if err != nil || next.ServerName != "" {
		t.Fatal("failed to restore default TLS server name")
	}
}

func TestStopInterruptsStalledSSHHandshake(t *testing.T) {
	fixture := httptest.NewTLSServer(nil)
	cert := fixture.TLS.Certificates[0]
	fixture.Close()
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	put := func(name string, data []byte) string {
		t.Helper()
		name = filepath.Join(dir, name)
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
		return name
	}
	ca := put("cert", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}))
	keyFile := put("key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	p := profile{User: "fixture", IdentityFile: keyFile, CA: ca, Cert: ca, Key: keyFile, KnownHosts: put("hosts", nil)}
	cfg := settings{endpoint: endpoint{SSHAddress: ln.Addr().String(), TargetAddress: "127.0.0.1:7443", Profile: "office"}, Reconnect: true}
	c := &control{row: t.Context(), profiles: map[string]profile{"office": p}, changed: make(chan struct{}),
		sessions: tcp.Sessions{Open: func(context.Context, tcp.Far) (tcp.Near, error) {
			t.Error("unauthenticated connection reached sessions.open")
			return tcp.Near{}, errors.New("unexpected open")
		}},
	}
	c.launch(cfg)
	defer c.Stop()
	ln.(*net.TCPListener).SetDeadline(time.Now().Add(3 * time.Second))
	peer, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	// Read the client's identification but never send a server identification.
	peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	var identification [256]byte
	if _, err := peer.Read(identification[:]); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := c.Stop(); err != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("stop did not cancel the SSH exchange promptly: %v", err)
	}
	s, _ := c.Status()
	if s.Phase != "stopped" || s.RetryAt != "" || s.RemoteAddress != "" {
		t.Fatalf("stale state: %+v", s)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.WaitForChange(ctx, s.Cursor); !errors.Is(err, context.Canceled) {
		t.Fatalf("watcher did not respect caller cancellation: %v", err)
	}
}
