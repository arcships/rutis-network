package rcp

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"github.com/arcships/rutis/go/rutis"
	"github.com/gorilla/websocket"
)

type device struct {
	cfg      config
	provide  Provider
	sessions tcp.Sessions
}

func (d *device) log(stage string, err error) {
	reason := "ok"
	if err != nil {
		reason = string(safeReason(err))
	}
	direction := "dial"
	if d.cfg.Listen != "" {
		direction = "accept"
	}
	log.Printf("rcp link=%s direction=%s stage=%s reason=%s", d.cfg.LinkID, direction, stage, reason)
}

func safeReason(err error) failure {
	var f failure
	if errors.As(err, &f) {
		return f
	}
	if errors.Is(err, context.Canceled) {
		return done
	}
	return transport
}

func (d *device) connected(ctx, row context.Context, conn *websocket.Conn, c Credentials) (bool, error) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	d.log("handshake", nil)
	ch, err := handshake(conn, c, d.cfg.MaxMessage, time.Duration(d.cfg.HeartbeatMs)*time.Millisecond)
	if err != nil {
		if !time.Now().Before(c.ExpiresAt) {
			err = expired
		}
		var rejection string
		switch err {
		case credential, expired:
			rejection = rejectCredential
		case incompatible:
			rejection = rejectVersion
		}
		if rejection != "" {
			conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = conn.WriteMessage(websocket.BinaryMessage, []byte(rejection))
		}
		return false, err
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	d.log("ready", nil)
	return true, serveSession(ctx, row, ch, d.sessions)
}

func dial(ctx context.Context, address string) (*websocket.Conn, error) {
	u, err := endpoint(address)
	if err != nil {
		return nil, err
	}
	// Cancel a pending WebSocket upgrade as well as DNS/TCP dialing.
	dialer := websocket.Dialer{HandshakeTimeout: handshakeTimeout}
	var stop func() bool
	dialer.NetDialContext = func(connectCtx context.Context, network, addr string) (net.Conn, error) {
		c, e := (&net.Dialer{}).DialContext(connectCtx, network, addr)
		if e == nil {
			stop = context.AfterFunc(ctx, func() { c.Close() })
		}
		return c, e
	}
	conn, resp, err := dialer.DialContext(ctx, u.String(), nil)
	if stop != nil {
		stop()
	}
	if err == nil {
		return conn, nil
	}
	if resp != nil {
		defer resp.Body.Close()
		switch resp.StatusCode {
		case 401, 403:
			return nil, credential
		case 410:
			return nil, expired
		case 404:
			return nil, offline
		case 429, 502, 503, 504:
			return nil, unavailable
		default:
			return nil, refused
		}
	}
	return nil, transport
}

func (d *device) runDial(ctx, row context.Context) error {
	return d.retryDial(ctx, func() (bool, error) {
		c, err := d.creds(ctx)
		if err != nil {
			return false, err
		}
		conn, err := dial(ctx, c.Address)
		if err != nil {
			return false, err
		}
		return d.connected(ctx, row, conn, c)
	})
}

func (d *device) retryDial(ctx context.Context, attempt func() (bool, error)) error {
	delay := 250 * time.Millisecond
	refreshed := false
	for ctx.Err() == nil {
		d.log("dial", nil)
		opened, err := attempt()
		d.log("close", err)
		if ctx.Err() != nil {
			return nil
		}
		if opened {
			delay = 250 * time.Millisecond
			refreshed = false
		}
		switch safeReason(err) {
		case credential, expired:
			if refreshed {
				return err
			}
			refreshed = true
		case transport, offline, unavailable, peerClosed, peerRestarted, oversized, ErrProviderUnavailable:
		default:
			return nil
		}
		d.log("reconnect", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		delay = min(2*delay, 30*time.Second)
	}
	return nil
}

func (d *device) listen(row *rutis.Ctx) error {
	u, err := endpoint(d.cfg.Listen)
	if err != nil || u.RawQuery != "" || u.ForceQuery {
		return errors.New("invalid RCP listener address")
	}
	addr := u.Host
	if u.Port() == "" {
		port := "443"
		if u.Scheme == "ws" {
			port = "80"
		}
		addr = net.JoinHostPort(u.Hostname(), port)
	}
	var cert tls.Certificate
	if u.Scheme == "wss" {
		cert, err = tls.LoadX509KeyPair(d.cfg.Cert, d.cfg.Key)
		if err != nil {
			return errors.New("invalid listener TLS key pair")
		}
	} else if d.cfg.Cert != "" || d.cfg.Key != "" {
		return errors.New("TLS settings require wss")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.New("RCP listener bind failed")
	}
	if u.Scheme == "wss" {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	}
	var active sync.Mutex
	upgrader := websocket.Upgrader{HandshakeTimeout: handshakeTimeout}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != u.EscapedPath() && !(u.EscapedPath() == "" && r.URL.EscapedPath() == "/") {
			http.NotFound(w, r)
			return
		}
		if !active.TryLock() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		defer active.Unlock()
		if row.Err() != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		c, err := d.creds(row)
		if errors.Is(err, ErrProviderUnavailable) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if err != nil || c.Role != "daemon" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			_, err = d.connected(row, row, conn, c)
			d.log("close", err)
		}
	})
	row.Go(func(ctx context.Context) error {
		stop := context.AfterFunc(ctx, func() { srv.Close() })
		defer stop()
		err := srv.Serve(ln)
		active.Lock()
		active.Unlock()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return transport
	})
	return nil
}

// Plugin builds the go:link-rcp definition. A nil provider reads the row's
// private credential file afresh on each outgoing or incoming connection.
func Plugin(provide Provider) *rutis.Definition {
	return rutis.Define(rutis.Plugin[config]{Name: "link-rcp", Inject: []string{"rutis/sessions"}, Apply: func(row *rutis.Ctx, c config) error {
		cfg, err := c.normalized()
		if err != nil {
			return err
		}
		p := provide
		if p == nil {
			if cfg.CredentialFile == "" {
				return errors.New("credentialFile required")
			}
			p = fileProvider(cfg.CredentialFile)
		}
		var sessions tcp.Sessions
		if err = row.Use("rutis/sessions", &sessions); err != nil {
			return err
		}
		d := &device{cfg: cfg, provide: p, sessions: sessions}
		if cfg.Listen != "" {
			return d.listen(row)
		}
		if cfg.Key != "" || cfg.Cert != "" {
			return errors.New("TLS key pair requires listen")
		}
		row.Go(func(ctx context.Context) error { return d.runDial(ctx, row) })
		return nil
	}})
}
