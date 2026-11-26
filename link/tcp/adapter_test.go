package tcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"testing"
	"time"
)

func TestListenerDeniesMissingClientCertificateBeforeOpen(t *testing.T) {
	// Use the standard library's disposable loopback TLS identity.
	fixture := httptest.NewTLSServer(nil)
	certificate := fixture.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	fixture.Close()
	server := &tls.Config{Certificates: []tls.Certificate{certificate}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert}
	local, peer := net.Pipe()
	defer peer.Close()
	result := make(chan error, 1)
	go func() {
		_, err := admit(t.Context(), local, server)
		result <- err
	}()
	peer.SetDeadline(time.Now().Add(3 * time.Second))
	client := tls.Client(peer, &tls.Config{RootCAs: roots, ServerName: "example.com", MaxVersion: tls.VersionTLS12})
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
		if got := retryDelay(errorCode(tc.err), 2*time.Second); got != tc.want {
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
	fixture := httptest.NewTLSServer(nil)
	defer fixture.Close()
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
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
	go func() { c.accept(ctx, listener, fixture.TLS, 1); close(stopped) }()
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
	client := tls.Client(second, &tls.Config{RootCAs: roots, ServerName: "example.com"})
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
