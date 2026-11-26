package grpclink

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/arcships/rutis-network/link/grpc/wire"
	"github.com/arcships/rutis-network/link/tcp"
	"github.com/arcships/rutis/go/rutis"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// Identity points to the trust roots and the local certificate/key pair.
type Identity struct {
	CAFile   string `json:"caFile"`
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
}

func (id Identity) TLSConfig() (*tls.Config, error) {
	ca, err := os.ReadFile(id.CAFile)
	if err != nil {
		return nil, errors.New("unable to read CA file")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid CA file")
	}
	cert, err := tls.LoadX509KeyPair(id.CertFile, id.KeyFile)
	if err != nil {
		return nil, errors.New("invalid certificate/key pair")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert},
		RootCAs: roots, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert,
	}, nil
}

type ClientConfig struct {
	Identity
	Address    string `json:"address"`
	ServerName string `json:"serverName"`
}

var ClientPlugin = rutis.Define(rutis.Plugin[ClientConfig]{
	Name: "link-grpc", Inject: []string{"rutis/sessions"},
	Schema: map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"address", "caFile", "certFile", "keyFile"},
		"properties": map[string]any{
			"address":    map[string]any{"type": "string", "minLength": 1},
			"serverName": map[string]any{"type": "string", "minLength": 1},
			"caFile":     map[string]any{"type": "string", "minLength": 1},
			"certFile":   map[string]any{"type": "string", "minLength": 1},
			"keyFile":    map[string]any{"type": "string", "minLength": 1},
		},
	},
	Apply: func(row *rutis.Ctx, cfg ClientConfig) error {
		_, err := MountClient(row, cfg)
		return err
	},
})

// Client manages the connection for one device row. An embedding plugin can
// call Start, Stop and Status locally. Unloading the row always stops retries.
type Client struct {
	row      context.Context
	config   ClientConfig
	sessions tcp.Sessions
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	statusMu sync.Mutex
	state    ClientStatus
}

type ClientStatus struct {
	Phase     string    `json:"phase"`
	LastError string    `json:"lastError,omitempty"`
	RetryAt   time.Time `json:"retryAt,omitzero"`
}

func MountClient(row *rutis.Ctx, cfg ClientConfig) (*Client, error) {
	host, port, err := net.SplitHostPort(cfg.Address)
	n, portErr := strconv.Atoi(port)
	if err != nil || host == "" || portErr != nil || n < 1 || n > 65535 {
		return nil, errors.New("address must be host:port")
	}
	var sessions tcp.Sessions
	if err := row.Use("rutis/sessions", &sessions); err != nil {
		return nil, err
	}
	c := &Client{row: row, config: cfg, sessions: sessions}
	row.Effect(func(context.Context) error {
		c.Stop()
		return nil
	})
	if err := c.Start(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) Status() ClientStatus {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	return c.state
}

func (c *Client) set(s ClientStatus) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	c.state = s
}

func (c *Client) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.row.Err(); err != nil {
		return err
	}
	if c.cancel != nil {
		return nil
	}
	tc, err := c.config.TLSConfig()
	if err != nil {
		return err
	}
	tc.ServerName = c.config.ServerName
	conn, err := grpc.NewClient("passthrough:///"+c.config.Address,
		grpc.WithTransportCredentials(credentials.NewTLS(tc)),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(MaxWireSize), grpc.MaxCallRecvMsgSize(MaxWireSize)))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(c.row)
	c.cancel = cancel
	done := make(chan struct{})
	c.done = done
	c.set(ClientStatus{Phase: "connecting"})
	go func() {
		defer close(done)
		defer conn.Close()
		c.run(ctx, conn)
	}()
	return nil
}

func (c *Client) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
		<-c.done
		c.cancel = nil
	}
	c.set(ClientStatus{Phase: "stopped"})
}

func (c *Client) run(ctx context.Context, conn *grpc.ClientConn) {
	backoff := 500 * time.Millisecond
	for ctx.Err() == nil {
		c.set(ClientStatus{Phase: "connecting"})
		started := time.Now()
		err := c.connect(ctx, conn)
		if ctx.Err() != nil {
			break
		}
		if time.Since(started) >= time.Minute {
			backoff = 500 * time.Millisecond
		}
		wait := backoff
		code := status.Code(err)
		if code == codes.PermissionDenied || code == codes.Unauthenticated {
			wait = 30 * time.Second
		}
		reason := "stream-ended"
		if err != nil {
			reason = code.String()
		}
		c.set(ClientStatus{Phase: "retrying", LastError: reason, RetryAt: time.Now().Add(wait)})
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}

func (c *Client) connect(parent context.Context, conn *grpc.ClientConn) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	// Bound the admission handshake independently of the long-lived stream.
	timer := time.AfterFunc(10*time.Second, cancel)
	defer timer.Stop()
	stream, err := wire.NewLinkClient(conn).Tunnel(ctx)
	if err != nil {
		return err
	}
	header, err := stream.Header()
	if err != nil {
		return err
	}
	if accepted := header.Get(admittedHeader); len(accepted) != 1 || accepted[0] != "1" {
		// A trailers-only rejection carries its gRPC status on Recv.
		if _, err := stream.Recv(); err != nil {
			return err
		}
		return status.Error(codes.FailedPrecondition, "missing admission acknowledgement")
	}
	if !timer.Stop() {
		return context.DeadlineExceeded
	}
	c.set(ClientStatus{Phase: "admitted"})
	return serveSession(ctx, c.row, stream, c.sessions, func(error) { cancel() })
}
