package tcp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

func testTLSConfigs(t *testing.T) (server, client *tls.Config) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	identity := []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}
	return &tls.Config{Certificates: identity, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert},
		&tls.Config{Certificates: identity, RootCAs: roots, ServerName: "example.com"}
}

func TestAdmitTLSRequiresMutualTLSConfiguration(t *testing.T) {
	server, _ := testTLSConfigs(t)
	server.ClientAuth = tls.NoClientCert
	local, peer := net.Pipe()
	defer peer.Close()
	if conn, err := AdmitTLS(t.Context(), local, server); conn != nil || !errors.Is(err, errCredentials) {
		t.Fatalf("expected invalid credentials, got %v, %v", conn, err)
	}
	peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("expected stream closure, got %v", err)
	}
}

func TestListenerDeniesMissingClientCertificateBeforeOpen(t *testing.T) {
	server, clientConfig := testTLSConfigs(t)
	clientConfig.Certificates = nil
	clientConfig.MaxVersion = tls.VersionTLS12
	local, peer := net.Pipe()
	defer peer.Close()
	result := make(chan error, 1)
	go func() {
		_, err := AdmitTLS(t.Context(), local, server)
		result <- err
	}()
	peer.SetDeadline(time.Now().Add(3 * time.Second))
	client := tls.Client(peer, clientConfig)
	if err := client.Handshake(); err == nil {
		t.Fatal("missing client certificate was accepted")
	}
	if err := <-result; err == nil {
		t.Fatal("server accepted missing client certificate")
	}
}

func TestRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want time.Duration
	}{
		{&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, 30 * time.Second},
		{&net.OpError{Op: "remote error", Err: tls.AlertError(116)}, 30 * time.Second},
		{&net.OpError{Op: "remote error", Err: tls.AlertError(70)}, 0},
		{tls.RecordHeaderError{}, 0},
		{errors.New("disconnected"), 2 * time.Second},
	} {
		if got := retryDelay(TransportErrorCode(tc.err), 2*time.Second); got != tc.want {
			t.Errorf("got %v, want %v", got, tc.want)
		}
	}
}

type countedListener struct {
	net.Listener
	accepted chan struct{}
}

func (l countedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted <- struct{}{}
	}
	return conn, err
}

func TestListenerBoundsPendingHandshakesOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server, clientConfig := testTLSConfigs(t)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := countedListener{ln, make(chan struct{}, 3)}
	opened := make(chan struct{}, 1)
	c := newControl(t.Context(), config{}, Sessions{Open: func(context.Context, Far) (Near, error) {
		opened <- struct{}{}
		return Near{End: func(context.Context, CloseReason) error { return nil }}, nil
	}}, false, settings{})
	stopped := make(chan struct{})
	go func() { c.accept(ctx, listener, server, 1); close(stopped) }()
	dial := func() net.Conn {
		conn, err := net.DialTimeout("tcp", ln.Addr().String(), 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	await := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for listener")
		}
	}
	first := dial()
	await(listener.accepted)
	second := dial()
	select {
	case <-listener.accepted:
		t.Fatal("accepted another socket while the handshake slot was occupied")
	case <-time.After(50 * time.Millisecond):
	}
	first.Close()
	await(listener.accepted)
	client := tls.Client(second, clientConfig)
	connect, end := context.WithTimeout(ctx, 3*time.Second)
	defer end()
	if err := client.HandshakeContext(connect); err != nil {
		t.Fatal(err)
	}
	await(opened)
	// An established session releases its admission slot. Stop while the
	// next socket is negotiating and the only slot is occupied.
	third := dial()
	await(listener.accepted)
	cancel()
	await(stopped)
	third.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := third.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("pending socket: %v", err)
	}
}
