package tcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/arcships/rutis/go/rutis"
)

const handshakeTimeout = 10 * time.Second
const initialDelay = 500 * time.Millisecond
const maxDelay = 30 * time.Second

var errCredentials = errors.New("invalid-credentials")

func (e config) credentials() (*tls.Config, error) {
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
	var dns *net.DNSError
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
	case errors.Is(err, syscall.EADDRINUSE):
		return "EADDRINUSE"
	case errors.Is(err, syscall.EACCES):
		return "EACCES"
	case errors.Is(err, syscall.ENETUNREACH):
		return "ENETUNREACH"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "EHOSTUNREACH"
	case errors.As(err, &dns) && dns.IsNotFound:
		return "ENOTFOUND"
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

// openDial admits a fresh socket and attaches it to the row's session service.
func (c *control) openDial(ctx context.Context, dest target) (<-chan error, error) {
	c.phase("dialing")
	identity, err := c.config.credentials()
	if err != nil {
		return nil, err
	}
	identity.ServerName = dest.tlsName()
	connectCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(connectCtx, "tcp", dest.address())
	if err != nil {
		return nil, err
	}
	c.update(func(s *snapshot) {
		s.Phase = "handshaking"
		s.LocalAddress = raw.LocalAddr().String()
		s.RemoteAddress = raw.RemoteAddr().String()
	})
	secure := tls.Client(raw, identity)
	if err = secure.HandshakeContext(connectCtx); err != nil {
		raw.Close()
		return nil, err
	}
	done, err := openStream(ctx, c.row, secure, c.sessions)
	if err == nil {
		c.phase("connected")
	}
	return done, err
}

func (c *control) dialLoop(ctx context.Context, dest target) {
	backoff := initialDelay
	for ctx.Err() == nil {
		done, err := c.openDial(ctx, dest)
		if err == nil {
			started := time.Now()
			err = <-done
			if time.Since(started) >= time.Minute {
				backoff = initialDelay
			}
		}
		if ctx.Err() != nil {
			return
		}
		code := errorCode(err)
		delay := retryDelay(code, backoff)
		c.mu.Lock()
		retry := c.state.Config.Reconnect && delay > 0
		c.state.LastError = code
		c.state.LocalAddress = ""
		c.state.RemoteAddress = ""
		if retry {
			c.state.Phase = "retrying"
			c.state.RetryCount++
			c.state.RetryAt = time.Now().Add(delay).UTC().Format(time.RFC3339Nano)
		} else {
			c.state.Phase = "failed"
			c.state.RetryAt = ""
		}
		c.notify()
		c.mu.Unlock()
		if !retry {
			fmt.Fprintf(os.Stderr, "link/tcp %s %s (stopped)\n", dest.address(), code)
			return
		}
		fmt.Fprintf(os.Stderr, "link/tcp %s %s (retry in %v)\n", dest.address(), code, delay)
		if !wait(ctx, delay) {
			return
		}
		backoff = min(maxDelay, backoff*2)
	}
}

func (c *control) bind(ctx context.Context, dest target) (net.Listener, *tls.Config, error) {
	c.phase("starting")
	identity, err := c.config.credentials()
	if err != nil {
		return nil, nil, err
	}
	identity.ClientAuth = tls.RequireAndVerifyClientCert
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", dest.address())
	if err != nil {
		return nil, nil, err
	}
	addr := listener.Addr().String()
	fmt.Fprintf(os.Stderr, "link/tcp listening %s\n", addr)
	c.update(func(s *snapshot) {
		s.Phase = "listening"
		s.LocalAddress = addr
		s.BoundPort = listener.Addr().(*net.TCPAddr).Port
	})
	return listener, identity, nil
}

func (c *control) serveConn(ctx context.Context, conn net.Conn, identity *tls.Config) error {
	secure := tls.Server(conn, identity)
	handshake, cancel := context.WithTimeout(ctx, handshakeTimeout)
	err := secure.HandshakeContext(handshake)
	cancel()
	if err != nil {
		conn.Close()
		return err
	}
	done, err := openStream(ctx, c.row, secure, c.sessions)
	if err != nil {
		return err
	}
	c.update(func(s *snapshot) { s.Connections++ })
	defer c.update(func(s *snapshot) { s.Connections-- })
	return <-done
}

func (c *control) accept(ctx context.Context, listener net.Listener, identity *tls.Config) {
	var clients sync.WaitGroup
	defer clients.Wait()
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
			code := errorCode(err)
			c.update(func(s *snapshot) {
				s.Phase = "retrying"
				s.LastError = code
				s.RetryCount++
				s.RetryAt = time.Now().Add(backoff).UTC().Format(time.RFC3339Nano)
			})
			fmt.Fprintf(os.Stderr, "link/tcp listen %s %s (retry in %v)\n", listener.Addr(), code, backoff)
			if !wait(ctx, backoff) {
				break
			}
			c.phase("listening")
			backoff = min(maxDelay, backoff*2)
			continue
		}
		backoff = initialDelay
		clients.Add(1)
		go func() {
			defer clients.Done()
			if err := c.serveConn(ctx, conn, identity); err != nil && ctx.Err() == nil {
				code := errorCode(err)
				c.update(func(s *snapshot) { s.LastError = code })
				fmt.Fprintf(os.Stderr, "link/tcp listen %s %s\n", listener.Addr(), code)
			}
		}()
	}
}

func apply(row *rutis.Ctx, cfg config, dial bool) error {
	var sessions Sessions
	if err := row.Use("rutis/sessions", &sessions); err != nil {
		return err
	}
	managed := cfg.ControlService != ""
	if !managed && cfg.Port == nil {
		return errors.New("port is required for static rows")
	}
	if !managed && cfg.AutoStart != nil {
		return errors.New("autoStart requires controlService")
	}
	if !dial && cfg.Reconnect != nil {
		return errors.New("listen only accepts port")
	}
	dest := target{Host: cfg.Host, Port: cfg.Port, ServerName: cfg.ServerName}
	if err := dest.validate(dial); err != nil {
		return err
	}
	if managed && !validServiceName(cfg.ControlService) {
		return errors.New("invalid controlService name")
	}
	reconnect := managed && dial
	if cfg.Reconnect != nil {
		reconnect = *cfg.Reconnect
	}
	c := newControl(row, cfg, sessions, dial, settings{target: dest, Reconnect: reconnect})
	if managed {
		if cfg.AutoStart == nil || *cfg.AutoStart {
			c.setRunning()
		}
		row.Effect(func(context.Context) error { return c.Stop() })
		row.Provide(cfg.ControlService, c)
		return nil
	}
	// Static single dials and initial binds surface failure during row load.
	if !dial {
		listener, identity, err := c.bind(row, dest)
		if err != nil {
			return err
		}
		row.Go(func(ctx context.Context) error {
			c.accept(ctx, listener, identity)
			return nil
		})
		return nil
	}
	if !reconnect {
		done, err := c.openDial(row, dest)
		if err != nil {
			return fmt.Errorf("link/tcp %s %s", dest.address(), errorCode(err))
		}
		row.Go(func(ctx context.Context) error {
			if err := <-done; err != nil && ctx.Err() == nil {
				return fmt.Errorf("link/tcp %s %s", dest.address(), errorCode(err))
			}
			return nil
		})
		return nil
	}
	row.Go(func(ctx context.Context) error {
		c.dialLoop(ctx, dest)
		return nil
	})
	return nil
}

var Dial = rutis.Define(rutis.Plugin[config]{
	Name: "link-tcp-dial", Inject: []string{"rutis/sessions"}, Schema: schema(true),
	Apply: func(row *rutis.Ctx, cfg config) error { return apply(row, cfg, true) },
})

var Listen = rutis.Define(rutis.Plugin[config]{
	Name: "link-tcp-listen", Inject: []string{"rutis/sessions"}, Schema: schema(false),
	Apply: func(row *rutis.Ctx, cfg config) error { return apply(row, cfg, false) },
})
