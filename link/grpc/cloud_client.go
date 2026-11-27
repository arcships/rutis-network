package grpclink

import (
	"context"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/arcships/rutis-network/link/grpc/wire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const maxRegionCandidates = 16
const registrationInterval = 10 * time.Second

// CloudConfig selects an authorized pair through the cloud directory.
type CloudConfig struct {
	Pair         string   `json:"pair"`
	Target       string   `json:"target"`
	Region       string   `json:"region,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Load         uint32   `json:"load,omitempty"`
	BearerFile   string   `json:"bearerFile,omitempty"`
}

func (cfg CloudConfig) registration() *wire.Registration {
	return &wire.Registration{Pair: cfg.Pair, Target: cfg.Target, Region: cfg.Region, Capabilities: cfg.Capabilities, Load: cfg.Load}
}

func (c *Client) resolve(parent context.Context, conn *grpc.ClientConn) (*wire.Route, error) {
	ctx, cancel := context.WithTimeout(parent, 6*time.Second)
	defer cancel()
	cfg := c.config.Cloud
	authCtx, err := cfg.authContext(ctx)
	if err != nil {
		return nil, err
	}
	directory := wire.NewCloudClient(conn)
	in := c.registration()
	if in.Region == "" {
		candidates, err := directory.Candidates(authCtx, in)
		if err != nil {
			return nil, err
		}
		if len(candidates.Regions) > maxRegionCandidates {
			return nil, status.Error(codes.FailedPrecondition, "too many regional endpoints")
		}
		if len(candidates.Regions) > 1 {
			in.Region = c.nearest(ctx, candidates.Regions)
		}
	}
	return directory.Resolve(authCtx, in)
}

// The first successful probe includes connection setup, TLS and one HTTP/2
// round trip. Cancel the others and keep the bearer credential at the directory.
func (c *Client) nearest(parent context.Context, regions []*wire.RegionEndpoint) string {
	ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
	defer cancel()
	results := make(chan string, len(regions))
	for _, r := range regions {
		go func() {
			name := ""
			if validAddress(r.Address) && label(r.ServerName) && label(r.Name) {
				conn, err := dial(c.config.Identity, r.Address, r.ServerName, c.config.Cloud.BearerFile == "")
				if err == nil {
					defer conn.Close()
					if _, err := wire.NewCloudRelayClient(conn).Ping(ctx, &wire.Probe{}); err == nil {
						name = r.Name
					}
				}
			}
			results <- name
		}()
	}
	for range regions {
		select {
		case name := <-results:
			if name != "" {
				return name
			}
		case <-ctx.Done():
			return ""
		}
	}
	return ""
}

func (cfg *CloudConfig) authContext(ctx context.Context) (context.Context, error) {
	authCtx := metadata.NewOutgoingContext(ctx, nil)
	if cfg.BearerFile != "" {
		token, err := os.ReadFile(cfg.BearerFile)
		value := strings.TrimSpace(string(token))
		if err != nil || value == "" || len(value) > 16384 || strings.ContainsAny(value, "\r\n") {
			return nil, status.Error(codes.Unauthenticated, "invalid bearer file")
		}
		authCtx = metadata.AppendToOutgoingContext(authCtx, "authorization", "Bearer "+value)
	}
	return authCtx, nil
}

// UpdateRegistration changes what the client reports at the next directory
// refresh. The established stream keeps its current pair, region and generation.
func (c *Client) UpdateRegistration(capabilities []string, load uint32) error {
	if c.config.Cloud == nil {
		return status.Error(codes.FailedPrecondition, "cloud configuration required")
	}
	in := c.config.Cloud.registration()
	in.Capabilities = slices.Clone(capabilities)
	in.Load = load
	if !validRegistration(in) {
		return status.Error(codes.InvalidArgument, "invalid registration")
	}
	c.registrationMu.Lock()
	defer c.registrationMu.Unlock()
	c.advertised = in
	return nil
}
func (c *Client) registration() *wire.Registration {
	c.registrationMu.RLock()
	defer c.registrationMu.RUnlock()
	if c.advertised != nil {
		return proto.CloneOf(c.advertised)
	}
	return c.config.Cloud.registration()
}
func (c *Client) refreshRegistration(ctx context.Context, conn *grpc.ClientConn, member *wire.Member) error {
	timer := time.NewTicker(registrationInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		call, cancel := context.WithTimeout(ctx, 3*time.Second)
		auth, err := c.config.Cloud.authContext(call)
		if err == nil {
			var current *wire.Member
			current, err = wire.NewCloudClient(conn).Register(auth, c.registration())
			if err == nil && !proto.Equal(member, current) {
				err = status.Error(codes.PermissionDenied, "member identity changed")
			}
		}
		cancel()
		if code := status.Code(err); code == codes.Unauthenticated || code == codes.PermissionDenied {
			return err
		}
	}
}
