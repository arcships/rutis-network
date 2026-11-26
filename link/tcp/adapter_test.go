package tcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
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
	opened := false
	result := make(chan error, 1)
	go func() {
		c := &control{row: t.Context(), sessions: Sessions{Open: func(context.Context, Far) (Near, error) {
			opened = true
			return Near{}, errors.New("admitted without a client certificate")
		}}}
		result <- c.serveConn(t.Context(), local, server)
	}()
	peer.SetDeadline(time.Now().Add(3 * time.Second))
	client := tls.Client(peer, &tls.Config{RootCAs: roots, ServerName: "example.com", MaxVersion: tls.VersionTLS12})
	if err := client.Handshake(); err == nil {
		t.Fatal("missing client certificate was accepted")
	}
	if err := <-result; err == nil || opened {
		t.Fatalf("server error %v, opened %v", err, opened)
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
