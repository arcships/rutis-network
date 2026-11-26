package tcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/arcships/rutis/go/rutis"
)

const handshakeTimeout = 10 * time.Second
const initialDelay = 500 * time.Millisecond
const maxDelay = 30 * time.Second

type endpoint struct {
	Port int    `json:"port"`
	CA   string `json:"ca"`
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

type dialConfig struct {
	endpoint
	Reconnect  bool   `json:"reconnect"`
	ServerName string `json:"servername"`
}

func schema(dial bool) map[string]any {
	minPort := 0
	props := map[string]any{}
	if dial {
		minPort = 1
		props["reconnect"] = map[string]any{"type": "boolean"}
		props["servername"] = map[string]any{"type": "string", "minLength": 1}
	}
	props["port"] = map[string]any{"type": "integer", "minimum": minPort, "maximum": 65535}
	for _, key := range []string{"ca", "cert", "key"} {
		props[key] = map[string]any{"type": "string", "minLength": 1}
	}
	return map[string]any{"type": "object", "required": []string{"port", "ca", "cert", "key"}, "properties": props}
}

func (e endpoint) address() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(e.Port)) }

var errCredentials = errors.New("invalid-credentials")

func (e endpoint) credentials() (*tls.Config, error) {
	pem, err := os.ReadFile(e.CA)
	if err != nil {
		return nil, fmt.Errorf("%w: CA: %w", errCredentials, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%w: no CA certificate", errCredentials)
	}
	pair, err := tls.LoadX509KeyPair(e.Cert, e.Key)
	if err != nil {
		return nil, fmt.Errorf("%w: key pair: %w", errCredentials, err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ClientCAs: pool, Certificates: []tls.Certificate{pair}}, nil
}

// The TCP TLS implementation exposes remote alerts inside net.OpError. Use
// crypto/tls's own alert descriptions for the RFC 8446 alert numbers.
func remoteAlert(err error, numbers ...tls.AlertError) bool {
	var op *net.OpError
	if !errors.As(err, &op) || op.Op != "remote error" {
		return false
	}
	for _, number := range numbers {
		if op.Err.Error() == number.Error() {
			return true
		}
	}
	return false
}

func errorCode(err error) string {
	var cert *tls.CertificateVerificationError
	var header tls.RecordHeaderError
	switch {
	case err == nil:
		return "connection-closed"
	case errors.Is(err, errCredentials):
		return errCredentials.Error()
	case errors.As(err, &cert), remoteAlert(err, 42, 43, 44, 45, 46, 48, 116):
		return "certificate-rejected"
	case errors.As(err, &header), remoteAlert(err, 70):
		return "incompatible-tls"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "ECONNREFUSED"
	case errors.Is(err, syscall.ECONNRESET):
		return "ECONNRESET"
	case errors.Is(err, syscall.EPIPE):
		return "EPIPE"
	case errors.Is(err, context.DeadlineExceeded), os.IsTimeout(err):
		return "ETIMEDOUT"
	case errors.Is(err, errTooLarge), errors.Is(err, errTruncated), errors.Is(err, errConcurrent):
		return err.Error()
	default:
		return "connection-failed"
	}
}

func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

func dial(ctx context.Context, config dialConfig, sessions Sessions) (<-chan error, error) {
	identity, err := config.credentials()
	if err != nil {
		return nil, err
	}
	identity.ServerName = config.ServerName
	if identity.ServerName == "" {
		identity.ServerName = "localhost"
	}
	connectCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	d := tls.Dialer{Config: identity}
	conn, err := d.DialContext(connectCtx, "tcp4", config.address())
	if err != nil {
		return nil, err
	}
	return Open(ctx, conn, sessions)
}

func retryDelay(code string, backoff time.Duration) time.Duration {
	switch code {
	case "incompatible-tls":
		return 0
	case "certificate-rejected", "invalid-credentials":
		return maxDelay
	default:
		return backoff
	}
}

func applyDial(row *rutis.Ctx, config dialConfig) error {
	var sessions Sessions
	if err := row.Use("rutis/sessions", &sessions); err != nil {
		return err
	}
	if !config.Reconnect {
		done, err := dial(row, config, sessions)
		if err != nil {
			return fmt.Errorf("link/tcp %s %s", config.address(), errorCode(err))
		}
		row.Go(func(ctx context.Context) error {
			if err := <-done; err != nil && ctx.Err() == nil {
				return fmt.Errorf("link/tcp %s %s", config.address(), errorCode(err))
			}
			return nil
		})
		return nil
	}
	row.Go(func(ctx context.Context) error {
		backoff := initialDelay
		for ctx.Err() == nil {
			done, err := dial(ctx, config, sessions)
			if err == nil {
				started := time.Now()
				err = <-done
				if time.Since(started) >= time.Minute {
					backoff = initialDelay
				}
			}
			if ctx.Err() != nil {
				break
			}
			code := errorCode(err)
			delay := retryDelay(code, backoff)
			if delay == 0 {
				return fmt.Errorf("link/tcp %s %s (stopped)", config.address(), code)
			}
			fmt.Fprintf(os.Stderr, "link/tcp %s %s (retry in %v)\n", config.address(), code, delay)
			if !wait(ctx, delay) {
				break
			}
			backoff = min(maxDelay, backoff*2)
		}
		return nil
	})
	return nil
}

func serveConn(ctx context.Context, conn net.Conn, identity *tls.Config, sessions Sessions) error {
	secure := tls.Server(conn, identity)
	handshake, cancel := context.WithTimeout(ctx, handshakeTimeout)
	err := secure.HandshakeContext(handshake)
	cancel()
	if err != nil {
		conn.Close()
		return err
	}
	done, err := Open(ctx, secure, sessions)
	if err != nil {
		return err
	}
	return <-done
}

func applyListen(row *rutis.Ctx, config endpoint) error {
	var sessions Sessions
	if err := row.Use("rutis/sessions", &sessions); err != nil {
		return err
	}
	identity, err := config.credentials()
	if err != nil {
		return err
	}
	identity.ClientAuth = tls.RequireAndVerifyClientCert
	listener, err := net.Listen("tcp4", config.address())
	if err != nil {
		return err
	}
	addr := listener.Addr()
	fmt.Fprintf(os.Stderr, "link/tcp listening %s\n", addr)
	row.Go(func(ctx context.Context) error {
		stop := context.AfterFunc(ctx, func() { listener.Close() })
		defer stop()
		defer listener.Close()
		backoff := initialDelay
		for ctx.Err() == nil {
			conn, err := listener.Accept()
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				fmt.Fprintf(os.Stderr, "link/tcp listen %s %s (retry in %v)\n", addr, errorCode(err), backoff)
				if !wait(ctx, backoff) {
					break
				}
				backoff = min(maxDelay, backoff*2)
				continue
			}
			backoff = initialDelay
			row.Go(func(ctx context.Context) error {
				if err := serveConn(ctx, conn, identity, sessions); err != nil && ctx.Err() == nil {
					return fmt.Errorf("link/tcp listen %s %s", addr, errorCode(err))
				}
				return nil
			})
		}
		return nil
	})
	return nil
}

var Dial = rutis.Define(rutis.Plugin[dialConfig]{Name: "link-tcp-dial", Inject: []string{"rutis/sessions"}, Schema: schema(true), Apply: applyDial})
var Listen = rutis.Define(rutis.Plugin[endpoint]{Name: "link-tcp-listen", Inject: []string{"rutis/sessions"}, Schema: schema(false), Apply: applyListen})
