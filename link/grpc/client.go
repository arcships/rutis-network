package grpclink

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/arcships/rutis-network/link/grpc/wire"
	"github.com/arcships/rutis-network/link/tcp"
	"github.com/arcships/rutis/go/rutis"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Identity points to the trust roots and the local certificate/key pair.
type Identity struct {
	CAFile   string `json:"caFile"`
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
}

func (id Identity) TLSConfig() (*tls.Config, error) {
	return id.tlsConfig(true)
}

func (id Identity) tlsConfig(requireCert bool) (*tls.Config, error) {
	ca, err := os.ReadFile(id.CAFile)
	if err != nil {
		return nil, errors.New("unable to read CA file")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid CA file")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert}
	if requireCert || id.CertFile != "" || id.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(id.CertFile, id.KeyFile)
		if err != nil {
			return nil, errors.New("invalid certificate/key pair")
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

type ClientConfig struct {
	Identity
	Cloud      *CloudConfig `json:"cloud,omitempty"`
	Address    string       `json:"address"`
	ServerName string       `json:"serverName"`
}

var ClientPlugin = rutis.Define(rutis.Plugin[ClientConfig]{
	Name: "link-grpc", Inject: []string{"rutis/sessions"},
	Schema: map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"address", "caFile"},
		"anyOf": []map[string]any{
			{"required": []string{"certFile", "keyFile"}},
			{"required": []string{"cloud"}, "properties": map[string]any{"cloud": map[string]any{"required": []string{"bearerFile"}}}},
		},
		"properties": map[string]any{
			"cloud": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"pair", "target"}, "properties": map[string]any{
				"pair":         map[string]any{"type": "string", "minLength": 1},
				"target":       map[string]any{"type": "string", "minLength": 1},
				"region":       map[string]any{"type": "string"},
				"bearerFile":   map[string]any{"type": "string", "minLength": 1},
				"load":         map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
				"capabilities": map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{"type": "string"}},
			}},
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
	row            context.Context
	config         ClientConfig
	sessions       tcp.Sessions
	mu             sync.Mutex
	cancel         context.CancelFunc
	done           chan struct{}
	statusMu       sync.Mutex
	state          ClientStatus
	registrationMu sync.RWMutex
	advertised     *wire.Registration
}

type ClientStatus struct {
	Reason     string    `json:"reason,omitempty"`
	Region     string    `json:"region,omitempty"`
	Generation uint64    `json:"generation,omitempty"`
	Phase      string    `json:"phase"`
	LastError  string    `json:"lastError,omitempty"`
	RetryAt    time.Time `json:"retryAt,omitzero"`
}

func MountClient(row *rutis.Ctx, cfg ClientConfig) (*Client, error) {
	if !validAddress(cfg.Address) {
		return nil, errors.New("address must be host:port")
	}
	if cloud := cfg.Cloud; cloud != nil {
		copy := *cloud
		copy.Capabilities = slices.Clone(cloud.Capabilities)
		cfg.Cloud = &copy
		if !validRegistration(cloud.registration()) {
			return nil, errors.New("invalid cloud registration")
		}
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

func (c *Client) newConn() (*grpc.ClientConn, error) {
	return dial(c.config.Identity, c.config.Address, c.config.ServerName, c.config.Cloud == nil || c.config.Cloud.BearerFile == "")
}

// DialControl opens the mTLS connection used by a regional agent or operator.
func DialControl(address, serverName string, id Identity) (*grpc.ClientConn, error) {
	if !validAddress(address) || !label(serverName) {
		return nil, errors.New("invalid directory address or server name")
	}
	return dial(id, address, serverName, true)
}

func dial(id Identity, address, name string, requireCert bool) (*grpc.ClientConn, error) {
	tc, err := id.tlsConfig(requireCert)
	if err != nil {
		return nil, err
	}
	tc.ServerName = name
	return grpc.NewClient("passthrough:///"+address,
		grpc.WithTransportCredentials(credentials.NewTLS(tc)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 5 * time.Minute, Timeout: 20 * time.Second}),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(MaxWireSize), grpc.MaxCallRecvMsgSize(MaxWireSize)))
}

func (c *Client) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.row.Err(); err != nil {
		return err
	}
	if c.cancel != nil {
		select {
		case <-c.done:
			c.cancel()
			c.cancel = nil
		default:
			return nil
		}
	}
	conn, err := c.newConn()
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
	for {
		c.set(ClientStatus{Phase: "connecting"})
		started := time.Now()
		// Start validated the first connection. Each retry uses the current
		// trust roots and identity, even after a partially installed rotation.
		var err error
		if conn == nil {
			conn, err = c.newConn()
			if err != nil {
				err = status.Error(codes.FailedPrecondition, err.Error())
			}
		}
		if err == nil {
			err = c.connect(ctx, conn)
			conn.Close()
			conn = nil
		}
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
		s := c.Status()
		s.LastError = reason
		for _, d := range status.Convert(err).Details() {
			if info, ok := d.(*errdetails.ErrorInfo); ok && info.Domain == "rutis.link.v1" {
				s.Reason = info.Reason
			}
		}
		if c.config.Cloud != nil && code == codes.Unimplemented {
			s.Phase = "incompatible"
			c.set(s)
			return
		}
		s.Phase = "retrying"
		s.RetryAt = time.Now().Add(wait)
		c.set(s)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}

func (c *Client) connect(parent context.Context, conn *grpc.ClientConn) error {
	timeout := 10 * time.Second
	open := wire.NewLinkClient(conn).Tunnel
	state := ClientStatus{}
	var member *wire.Member
	if cfg := c.config.Cloud; cfg != nil {
		route, err := c.resolve(parent, conn)
		if err != nil {
			return err
		}
		member = route.Member
		if !label(member.GetTenant()) || !label(member.GetId()) || !validAddress(route.Address) || !label(route.ServerName) || !label(route.Region) || len(route.Ticket) != 64 || route.Generation == 0 {
			return status.Error(codes.FailedPrecondition, "invalid route")
		}
		regional, err := dial(c.config.Identity, route.Address, route.ServerName, cfg.BearerFile == "")
		if err != nil {
			return status.Error(codes.FailedPrecondition, "invalid regional credentials")
		}
		defer regional.Close()
		parent = metadata.NewOutgoingContext(parent, metadata.Pairs(versionHeader, "1", pairHeader, cfg.Pair, targetHeader, cfg.Target, generationHeader, strconv.FormatUint(route.Generation, 10), ticketHeader, route.Ticket))
		open = wire.NewCloudRelayClient(regional).Tunnel
		timeout = 45 * time.Second
		state = ClientStatus{Phase: "pairing", Region: route.Region, Generation: route.Generation}
		c.set(state)
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	// Bound the admission handshake independently of the long-lived stream.
	timer := time.AfterFunc(timeout, func() { cancel(context.DeadlineExceeded) })
	defer timer.Stop()
	stream, err := open(ctx)
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
	if c.config.Cloud != nil {
		ended := make(chan struct{})
		go func() {
			defer close(ended)
			if err := c.refreshRegistration(ctx, conn, member); err != nil {
				cancel(err)
			}
		}()
		defer func() { cancel(nil); <-ended }()
	}
	state.Phase = "admitted"
	c.set(state)
	err = serveSession(ctx, c.row, stream, c.sessions, cancel)
	if cause := context.Cause(ctx); status.Code(cause) == codes.PermissionDenied || status.Code(cause) == codes.Unauthenticated {
		return cause
	}
	return err
}
