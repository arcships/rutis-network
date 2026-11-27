package wslink

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"github.com/arcships/rutis/go/rutis"
	"github.com/gorilla/websocket"
)

type config struct {
	Address   string `json:"address"`
	TokenFile string `json:"tokenFile"`
	CA        string `json:"ca,omitempty"`
	Cert      string `json:"cert,omitempty"`
	Key       string `json:"key,omitempty"`
}

var bearer = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)

func (c config) endpoint() (*url.URL, string, error) {
	u, err := url.Parse(c.Address)
	if err != nil || u.Scheme != "wss" || u.Hostname() == "" || u.User != nil || u.ForceQuery || u.RawQuery != "" || strings.Contains(c.Address, "#") {
		return nil, "", errors.New("address requires a wss URL with a host and optional path")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	data, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return nil, "", fmt.Errorf("read tokenFile: %w", err)
	}
	token := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if !bearer.MatchString(token) {
		return nil, "", errors.New("tokenFile must contain one bearer token")
	}
	return u, token, nil
}

func schema(listen bool) map[string]any {
	text := map[string]any{"type": "string", "minLength": 1}
	props := map[string]any{"address": text, "tokenFile": text}
	required := []string{"address", "tokenFile"}
	if listen {
		props["cert"], props["key"] = text, text
		required = append(required, "cert", "key")
	} else {
		props["ca"] = text
	}
	return map[string]any{"type": "object", "required": required, "properties": props, "additionalProperties": false}
}

type listener struct {
	path     string
	tokenSum [sha256.Size]byte
	sessions tcp.Sessions
	ctx      context.Context
	row      context.Context
	active   sync.Mutex
}

func (l *listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.EscapedPath() != l.path || r.URL.RawQuery != "" || r.URL.ForceQuery {
		http.NotFound(w, r)
		return
	}
	value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	sum := sha256.Sum256([]byte(value))
	if !ok || len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare(sum[:], l.tokenSum[:]) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !slices.Contains(websocket.Subprotocols(r), protocol) {
		http.Error(w, "expected "+protocol, http.StatusBadRequest)
		return
	}
	// Go's sessions.open exposes one addressable session handle per row.
	if !l.active.TryLock() {
		http.Error(w, "listener busy", http.StatusServiceUnavailable)
		return
	}
	defer l.active.Unlock()
	if l.ctx.Err() != nil {
		http.Error(w, "listener stopped", http.StatusServiceUnavailable)
		return
	}
	upgrade := websocket.Upgrader{Subprotocols: []string{protocol}, HandshakeTimeout: 5 * time.Second}
	conn, err := upgrade.Upgrade(w, r, nil)
	if err == nil {
		_ = serveSession(l.ctx, l.row, conn, l.sessions)
	}
}

var Listen = rutis.Define(rutis.Plugin[config]{
	Name: "link-websocket-listen", Inject: []string{"rutis/sessions"}, Schema: schema(true),
	Apply: func(row *rutis.Ctx, cfg config) error {
		u, token, err := cfg.endpoint()
		if err != nil {
			return err
		}
		if cfg.CA != "" {
			return errors.New("ca is a dial setting")
		}
		cert, err := tls.LoadX509KeyPair(cfg.Cert, cfg.Key)
		if err != nil {
			return err
		}
		var sessions tcp.Sessions
		if err = row.Use("rutis/sessions", &sessions); err != nil {
			return err
		}
		port := u.Port()
		if port == "" {
			port = "443"
		}
		socket, err := net.Listen("tcp", net.JoinHostPort(u.Hostname(), port))
		if err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(row)
		l := &listener{path: u.EscapedPath(), tokenSum: sha256.Sum256([]byte(token)), sessions: sessions, ctx: ctx, row: row}
		server := &http.Server{
			Handler: l,
			TLSConfig: &tls.Config{
				MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert},
				NextProtos: []string{"http/1.1"},
			},
			ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
		}
		row.Go(func(context.Context) error {
			stop := context.AfterFunc(ctx, func() { _ = server.Close() })
			err := server.ServeTLS(socket, "", "")
			stop()
			cancel()
			_ = server.Close()
			l.active.Lock() // ServeTLS does not join hijacked connections.
			l.active.Unlock()
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		})
		return nil
	},
})

func clientTLS(ca string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = x509.NewCertPool()
		if !cfg.RootCAs.AppendCertsFromPEM(pem) {
			return nil, errors.New("ca must contain a PEM certificate")
		}
	}
	return cfg, nil
}

const maxDelay = 30 * time.Second

func dial(ctx context.Context, address, token string, tlsConfig *tls.Config) (*websocket.Conn, time.Duration, error) {
	dialer := websocket.Dialer{TLSClientConfig: tlsConfig, Subprotocols: []string{protocol}, HandshakeTimeout: 8 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, address, http.Header{"Authorization": {"Bearer " + token}})
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		var failure *tls.CertificateVerificationError
		if errors.As(err, &failure) || resp != nil && (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized) {
			return nil, maxDelay, err
		}
		if resp != nil && (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUpgradeRequired) {
			return nil, 0, errIncompatible
		}
		return nil, 0, err
	}
	if conn.Subprotocol() != protocol {
		_ = conn.Close()
		return nil, 0, errIncompatible
	}
	return conn, 0, nil
}

var Dial = rutis.Define(rutis.Plugin[config]{
	Name: "link-websocket-dial", Inject: []string{"rutis/sessions"}, Schema: schema(false),
	Apply: func(row *rutis.Ctx, cfg config) error {
		u, token, err := cfg.endpoint()
		if err != nil {
			return err
		}
		if cfg.Cert != "" || cfg.Key != "" {
			return errors.New("cert and key are listen settings")
		}
		identity, err := clientTLS(cfg.CA)
		if err != nil {
			return err
		}
		var sessions tcp.Sessions
		if err = row.Use("rutis/sessions", &sessions); err != nil {
			return err
		}
		row.Go(func(ctx context.Context) error {
			const initial = 500 * time.Millisecond
			backoff := initial
			for ctx.Err() == nil {
				conn, minimum, err := dial(ctx, u.String(), token, identity)
				if err == nil {
					opened := time.Now()
					err = serveSession(ctx, row, conn, sessions)
					if time.Since(opened) >= time.Minute {
						backoff = initial
					}
				}
				if errors.Is(err, errIncompatible) {
					return err
				}
				var closed *websocket.CloseError
				if errors.As(err, &closed) {
					if closed.Code == 4003 {
						minimum = maxDelay
					}
					// The peer controls the close reason. Log only the wire code.
					err = fmt.Errorf("websocket closed with code %d", closed.Code)
				}
				if err != nil && ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "link-websocket-dial %s: %v\n", u.Host, err)
				}
				select {
				case <-ctx.Done():
				case <-time.After(max(backoff, minimum)):
				}
				backoff = min(backoff*2, maxDelay)
			}
			return ctx.Err()
		})
		return nil
	},
})
