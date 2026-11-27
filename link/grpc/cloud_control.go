package grpclink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arcships/rutis-network/link/grpc/wire"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type CloudRegion struct {
	Name       string `json:"name"`
	Address    string `json:"address"`
	ServerName string `json:"serverName"`
	AgentName  string `json:"agentName"`
}
type CloudPair struct {
	Tenant  string   `json:"tenant"`
	ID      string   `json:"id"`
	Members []string `json:"members"`
	Regions []string `json:"regions"` // Ordered data-residency allowlist.
}
type ControlConfig struct {
	StateFile string         `json:"stateFile"`
	Members   []CloudMember  `json:"members"`
	Pairs     []CloudPair    `json:"pairs"`
	Regions   []CloudRegion  `json:"regions"`
	MaxPairs  map[string]int `json:"maxPairs"`
	AdminCert string         `json:"adminCert"`
	JWT       *JWTPolicy     `json:"jwt,omitempty"`
}
type pairKey struct{ tenant, pair string }
type regionState struct {
	CloudRegion
	until  time.Time
	report *wire.RegionReport
}
type issuedRoute struct {
	ticket     string
	generation uint64
	cert       []byte
	expires    time.Time
}
type placement struct {
	CloudPair
	region, instance string
	reserved         time.Time
	routes           map[string]issuedRoute
}

// Control is the single route authority in the reference deployment. A cold
// authority waits one lease period before assigning regions, fencing old agents.
type Control struct {
	wire.UnimplementedCloudServer
	mu      sync.Mutex
	auth    *cloudAuth
	admin   string
	hosts   map[memberID]*wire.HostStatus
	pairs   map[pairKey]*placement
	regions map[string]*regionState
	quota   map[string]int
	lease   time.Duration
	ready   time.Time
	db      *bolt.DB
}

const regionLease = 12 * time.Second
const ticketTTL = 30 * time.Second

func NewControl(cfg ControlConfig) (*Control, error) { return newControl(cfg, regionLease) }
func newControl(cfg ControlConfig, lease time.Duration) (*Control, error) {
	auth, err := newCloudAuth(cfg.Members, cfg.JWT)
	if err != nil {
		return nil, err
	}
	c := &Control{
		auth: auth, admin: cfg.AdminCert, lease: lease, quota: maps.Clone(cfg.MaxPairs),
		hosts: map[memberID]*wire.HostStatus{}, pairs: map[pairKey]*placement{},
		regions: map[string]*regionState{},
	}
	if c.admin != "" && !label(c.admin) {
		return nil, errors.New("invalid operator certificate name")
	}
	tenants := map[string]bool{}

	agents := map[string]bool{}
	for _, r := range cfg.Regions {
		if strings.EqualFold(r.AgentName, c.admin) || !label(r.Name) || !validAddress(r.Address) || !label(r.ServerName) || !label(r.AgentName) || agents[strings.ToLower(r.AgentName)] || c.regions[r.Name] != nil {
			return nil, errors.New("invalid or duplicate cloud region")
		}
		agents[strings.ToLower(r.AgentName)] = true
		c.regions[r.Name] = &regionState{CloudRegion: r}
	}
	for _, m := range cfg.Members {
		id := memberID{m.Tenant, m.ID}
		if m.CertName != "" && (!label(m.CertName) || strings.EqualFold(m.CertName, c.admin) || agents[strings.ToLower(m.CertName)]) {
			return nil, errors.New("invalid or overlapping member certificate name")
		}
		if !label(m.Tenant) || !label(m.ID) {
			return nil, errors.New("invalid cloud member")
		}
		if _, ok := c.hosts[id]; ok {
			return nil, errors.New("duplicate cloud member")
		}
		tenants[m.Tenant] = true
		c.hosts[id] = nil
	}
	for _, p := range cfg.Pairs {
		key := pairKey{p.Tenant, p.ID}
		if !label(p.ID) || len(p.Members) != 2 || p.Members[0] == p.Members[1] || c.pairs[key] != nil || len(p.Regions) == 0 || len(p.Regions) > maxRegionCandidates {
			return nil, errors.New("invalid or duplicate cloud pair")
		}
		for _, id := range p.Members {
			if _, ok := c.hosts[memberID{p.Tenant, id}]; !ok {
				return nil, errors.New("unknown pair member")
			}
		}
		seen := map[string]bool{}
		for _, name := range p.Regions {
			if c.regions[name] == nil || seen[name] {
				return nil, errors.New("unknown or duplicate allowed region")
			}
			seen[name] = true
		}
		p.Members = slices.Clone(p.Members)
		p.Regions = slices.Clone(p.Regions)
		c.pairs[key] = &placement{CloudPair: p, routes: map[string]issuedRoute{}}
	}
	for tenant, n := range c.quota {
		if !tenants[tenant] || n < 1 {
			return nil, errors.New("invalid tenant quota")
		}
	}
	if len(c.pairs) == 0 {
		return nil, errors.New("at least one cloud pair required")
	}
	if cfg.StateFile == "" {
		return nil, errors.New("control stateFile is required")
	}
	c.db, err = bolt.Open(cfg.StateFile, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	c.ready = time.Now().Add(lease)
	return c, nil
}

// Close releases the exclusive generation store after the RPC server stops.
func (c *Control) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil {
		return nil
	}
	err := c.db.Close()
	c.db = nil
	return err
}

func (c *Control) nextGeneration() (uint64, error) {
	var next uint64
	err := c.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("routes"))
		if err != nil {
			return err
		}
		next, err = bucket.NextSequence()
		return err
	})
	return next, err
}
func randomTicket() string {
	var b [32]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
func pairError(code codes.Code, reason, pair, target string, generation uint64) error {
	s := status.New(code, reason)
	d, err := s.WithDetails(&errdetails.ErrorInfo{Domain: "rutis.link.v1", Reason: reason, Metadata: map[string]string{"pair": pair, "target": target, "generation": strconv.FormatUint(generation, 10)}})
	if err != nil {
		return s.Err()
	}
	return d.Err()
}

func (c *Control) Heartbeat(ctx context.Context, r *wire.RegionReport) (*wire.Lease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.db == nil {
		return nil, status.Error(codes.Unavailable, "authority closed")
	}
	g := c.regions[r.Region]
	if g == nil || !namedCert(ctx, g.AgentName) {
		return nil, status.Error(codes.PermissionDenied, "regional agent certificate required")
	}
	if !label(r.Instance) || len(r.Pairs) > len(c.pairs) || len(r.Errors) > 32 {
		return nil, status.Error(codes.InvalidArgument, "invalid region report")
	}
	if now.Before(c.ready) {
		return nil, status.Error(codes.Unavailable, "authority starting")
	}
	if now.Before(g.until) && g.report.Instance != r.Instance {
		return nil, status.Error(codes.AlreadyExists, "regional lease held")
	}
	g.report = proto.CloneOf(r)
	g.until = now.Add(c.lease)
	for _, active := range r.Pairs {
		p := c.pairs[pairKey{active.GetTenant(), active.GetPair()}]
		if p != nil && p.region == r.Region && p.instance == r.Instance {
			p.reserved = now.Add(ticketTTL + c.lease)
		}
	}
	return &wire.Lease{Milliseconds: c.lease.Milliseconds()}, nil
}
func (c *Control) placed(p *placement, now time.Time) bool {
	g := c.regions[p.region]
	return g != nil && now.Before(p.reserved) && now.Before(g.until) && g.report.Instance == p.instance
}
func (c *Control) choose(p *placement, preferred string, now time.Time) (*regionState, error) {
	if c.placed(p, now) {
		return c.regions[p.region], nil
	}
	if limit := c.quota[p.Tenant]; limit > 0 {
		n := 0
		for _, other := range c.pairs {
			if other.Tenant == p.Tenant && c.placed(other, now) {
				n++
			}
		}
		if n >= limit {
			return nil, status.Error(codes.ResourceExhausted, "tenant pair quota")
		}
	}
	order := p.Regions
	if slices.Contains(order, preferred) {
		order = append([]string{preferred}, order...)
	}
	for _, name := range order {
		g := c.regions[name]
		if now.Before(g.until) {
			p.region, p.instance = g.Name, g.report.Instance
			clear(p.routes)
			return g, nil
		}
	}
	return nil, pairError(codes.Unavailable, "NO_ALLOWED_REGION", p.ID, "", 0)
}

// registration returns a pair authorized for this TLS principal. The caller holds mu.
func (c *Control) registration(member memberID, in *wire.Registration) (*placement, error) {
	if c.db == nil {
		return nil, status.Error(codes.Unavailable, "authority closed")
	}
	if !validRegistration(in) {
		return nil, status.Error(codes.InvalidArgument, "invalid registration")
	}
	p := c.pairs[pairKey{member.tenant, in.Pair}]
	if p == nil || in.Target == member.id || !slices.Contains(p.Members, member.id) || !slices.Contains(p.Members, in.Target) {
		return nil, status.Error(codes.PermissionDenied, "pair or target denied")
	}
	return p, nil
}

// Register refreshes a member's advertised capacity without issuing a new ticket.
func (c *Control) Register(ctx context.Context, in *wire.Registration) (*wire.Member, error) {
	member, err := c.auth.member(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.registration(member, in); err != nil {
		return nil, err
	}
	return c.record(member, in, time.Now()), nil
}
func (c *Control) record(id memberID, in *wire.Registration, now time.Time) *wire.Member {
	member := &wire.Member{Tenant: id.tenant, Id: id.id}
	c.hosts[id] = &wire.HostStatus{Member: member, Registration: proto.CloneOf(in), SeenUnix: now.Unix()}
	return proto.CloneOf(member)
}

func (c *Control) Candidates(ctx context.Context, in *wire.Registration) (*wire.RegionList, error) {
	member, err := c.auth.member(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, err := c.registration(member, in)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	names := p.Regions
	if c.placed(p, now) {
		names = []string{p.region}
	}
	list := &wire.RegionList{}
	for _, name := range names {
		g := c.regions[name]
		if now.Before(g.until) {
			list.Regions = append(list.Regions, &wire.RegionEndpoint{Name: g.Name, Address: g.Address, ServerName: g.ServerName})
		}
	}
	return list, nil
}

func (c *Control) Resolve(ctx context.Context, in *wire.Registration) (*wire.Route, error) {
	member, err := c.auth.member(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, err := c.registration(member, in)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	c.record(member, in, now)
	g, err := c.choose(p, in.Region, now)
	if err != nil {
		return nil, err
	}
	generation, err := c.nextGeneration()
	if err != nil {
		return nil, status.Error(codes.Unavailable, "generation store unavailable")
	}
	t := issuedRoute{randomTicket(), generation, certificateHash(ctx), now.Add(ticketTTL)}
	p.routes[member.id] = t
	p.reserved = now.Add(ticketTTL + c.lease)
	return &wire.Route{Address: g.Address, ServerName: g.ServerName, Region: g.Name, Ticket: t.ticket, Generation: t.generation, Member: &wire.Member{Tenant: member.tenant, Id: member.id}}, nil
}
func (c *Control) Admit(ctx context.Context, in *wire.Claim) (*wire.Member, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil {
		return nil, status.Error(codes.Unavailable, "authority closed")
	}
	g := c.regions[in.Region]
	if g == nil || !namedCert(ctx, g.AgentName) {
		return nil, status.Error(codes.PermissionDenied, "regional agent certificate required")
	}
	now := time.Now()
	if !now.Before(g.until) || g.report.Instance != in.Instance {
		return nil, status.Error(codes.Unavailable, "regional lease expired")
	}
	// A ticket is only usable by its assigned agent, pair, target and TLS peer.
	for _, p := range c.pairs {
		if p.ID != in.Pair || p.region != g.Name || p.instance != in.Instance || !slices.Contains(p.Members, in.Target) {
			continue
		}
		for member, t := range p.routes {
			if member != in.Target && t.generation == in.Generation && t.ticket == in.Ticket && now.Before(t.expires) && bytes.Equal(t.cert, in.CertificateHash) {
				delete(p.routes, member)
				return &wire.Member{Tenant: p.Tenant, Id: member}, nil
			}
		}
	}
	return nil, status.Error(codes.PermissionDenied, "invalid or spent route")
}
func (c *Control) Inspect(ctx context.Context, _ *wire.InspectRequest) (*wire.CloudStatus, error) {
	if c.admin == "" || !namedCert(ctx, c.admin) {
		return nil, status.Error(codes.PermissionDenied, "operator certificate required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := &wire.CloudStatus{}
	for _, host := range c.hosts {
		if host != nil {
			snapshot := proto.CloneOf(host)
			snapshot.Recent = time.Since(time.Unix(host.SeenUnix, 0)) < 3*registrationInterval
			out.Hosts = append(out.Hosts, snapshot)
		}
	}
	for _, g := range c.regions {
		report := &wire.RegionReport{Region: g.Name}
		if g.report != nil {
			report = proto.CloneOf(g.report)
		}
		out.Regions = append(out.Regions, &wire.RegionStatus{Report: report, Available: time.Now().Before(g.until)})
	}
	return out, nil
}

func validRegistration(in *wire.Registration) bool {
	if !label(in.Pair) || !label(in.Target) || (in.Region != "" && !label(in.Region)) || len(in.Capabilities) > 32 || in.Load > 100 {
		return false
	}
	for _, s := range in.Capabilities {
		if !label(s) {
			return false
		}
	}
	return true
}
