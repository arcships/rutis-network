package grpclink

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arcships/rutis-network/link/grpc/wire"
	"github.com/arcships/rutis-network/link/tcp"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type regionTest struct {
	*Relay
	blocked    atomic.Bool
	probeDelay atomic.Int64
}

func (r *regionTest) Ping(ctx context.Context, p *wire.Probe) (*wire.Probe, error) {
	if delay := time.Duration(r.probeDelay.Load()); delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return r.Relay.Ping(ctx, p)
}

type heartbeatGate struct {
	wire.CloudClient
	blocked *atomic.Bool
}

func (g heartbeatGate) Heartbeat(ctx context.Context, r *wire.RegionReport, opts ...grpc.CallOption) (*wire.Lease, error) {
	if g.blocked.Load() {
		return nil, status.Error(codes.Unavailable, "test partition")
	}
	return g.CloudClient.Heartbeat(ctx, r, opts...)
}

type cloudTest struct {
	address   string
	a, b, ops Identity
	directory wire.CloudClient
	regions   map[string]*regionTest
}

func until(t *testing.T, test func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
		if test() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition deadline exceeded")
}
func cloudFixture(t *testing.T, pairWait time.Duration) *cloudTest {
	t.Helper()
	return cloudFixtureWithCapacity(t, pairWait, 2)
}
func cloudFixtureWithCapacity(t *testing.T, pairWait time.Duration, maxStreams int) *cloudTest {
	t.Helper()
	issue := testIssuer(t)
	srv := issue("cloud.test", x509.ExtKeyUsageServerAuth)
	member := func(name string) Identity { return issue(name, x509.ExtKeyUsageClientAuth) }
	x := &cloudTest{a: member("a.test"), b: member("b.test"), ops: member("ops.test"), regions: map[string]*regionTest{}}
	listen := func() net.Listener {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		return l
	}
	start := func(l net.Listener, register func(*grpc.Server)) {
		cfg, err := srv.TLSConfig()
		if err != nil {
			t.Fatal(err)
		}
		cfg.ClientAuth = tls.VerifyClientCertIfGiven
		server := grpc.NewServer(grpc.Creds(credentials.NewTLS(cfg)), grpc.MaxRecvMsgSize(MaxWireSize), grpc.MaxSendMsgSize(MaxWireSize))
		register(server)
		go server.Serve(l)
		t.Cleanup(server.Stop)
	}
	cfg := ControlConfig{StateFile: filepath.Join(t.TempDir(), "routes.db"),
		Members: []CloudMember{{Tenant: "acme", ID: "mobile", CertName: "a.test"}, {Tenant: "acme", ID: "worker", CertName: "b.test"}, {Tenant: "two", ID: "mobile", KeySHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("external")))}, {Tenant: "two", ID: "worker", KeySHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("external-worker")))}},
		Pairs:   []CloudPair{{"acme", "jobs", []string{"mobile", "worker"}, []string{"east", "west"}}, {"acme", "only-east", []string{"mobile", "worker"}, []string{"east"}}, {"two", "jobs", []string{"mobile", "worker"}, []string{"west"}}}, MaxPairs: map[string]int{"acme": 1}, AdminCert: "ops.test"}
	directory := listen()
	x.address = directory.Addr().String()
	listeners := map[string]net.Listener{}
	for _, name := range []string{"east", "west"} {
		l := listen()
		listeners[name] = l
		cfg.Regions = append(cfg.Regions, CloudRegion{name, l.Addr().String(), "cloud.test", name + ".test"})
	}
	control, err := newControl(cfg, 700*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.Close() })
	start(directory, func(s *grpc.Server) { wire.RegisterCloudServer(s, control) })
	x.directory = wire.NewCloudClient(testConn(t, x.address, x.ops))
	for _, r := range cfg.Regions {
		agent := &regionTest{}
		d := wire.NewCloudClient(testConn(t, x.address, member(r.AgentName)))
		agent.Relay, err = NewRelay(heartbeatGate{d, &agent.blocked}, r.Name, maxStreams, pairWait)
		if err != nil {
			t.Fatal(err)
		}
		start(listeners[r.Name], func(s *grpc.Server) { wire.RegisterCloudRelayServer(s, agent) })
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); agent.Run(ctx) }()
		t.Cleanup(func() { cancel(); <-done })
		x.regions[r.Name] = agent
	}
	until(t, func() bool {
		for _, r := range x.regions {
			if _, err := r.Ping(t.Context(), &wire.Probe{}); err != nil {
				return false
			}
		}
		return true
	})
	return x
}
func (x *cloudTest) route(t *testing.T, id Identity, target, region string) *wire.Route {
	t.Helper()
	r, err := wire.NewCloudClient(testConn(t, x.address, id)).Resolve(t.Context(), &wire.Registration{Pair: "jobs", Target: target, Region: region})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func openTestStream(t *testing.T, ctx context.Context, r *wire.Route, target string, cc *grpc.ClientConn) wire.CloudRelay_TunnelClient {
	t.Helper()
	return openPairStream(t, ctx, r, "jobs", target, cc)
}
func openPairStream(t *testing.T, ctx context.Context, r *wire.Route, pair, target string, cc *grpc.ClientConn) wire.CloudRelay_TunnelClient {
	t.Helper()
	ctx = metadata.AppendToOutgoingContext(ctx, versionHeader, "1", pairHeader, pair, targetHeader, target, generationHeader, strconv.FormatUint(r.Generation, 10), ticketHeader, r.Ticket)
	s, err := wire.NewCloudRelayClient(cc).Tunnel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func expectReason(t *testing.T, err error, code codes.Code, reason string) {
	t.Helper()
	if status.Code(err) == code {
		for _, d := range status.Convert(err).Details() {
			if info, ok := d.(*errdetails.ErrorInfo); ok && info.Reason == reason {
				return
			}
		}
	}
	t.Fatalf("want %s/%s, got %v", code, reason, err)
}
func TestCloudRelayPairing(t *testing.T) {
	x := cloudFixture(t, 2*time.Second)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	a := x.route(t, x.a, "worker", "east")
	b := x.route(t, x.b, "mobile", "west")
	if a.Region != "east" || b.Region != a.Region {
		t.Fatal("pair lost region affinity")
	}
	ac, bc := testConn(t, a.Address, x.a), testConn(t, b.Address, x.b)
	// A copied ticket requires the same member certificate.
	if _, err := openTestStream(t, ctx, a, "worker", bc).Recv(); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	left, right := openTestStream(t, ctx, a, "worker", ac), openTestStream(t, ctx, b, "mobile", bc)
	for _, s := range []wire.CloudRelay_TunnelClient{left, right} {
		h, err := s.Header()
		if err != nil || len(h.Get(admittedHeader)) != 1 {
			t.Fatal(h, err)
		}
	}
	if _, err := openTestStream(t, ctx, a, "worker", ac).Recv(); status.Code(err) != codes.PermissionDenied {
		t.Fatal("replay", err)
	}
	payload := bytes.Repeat([]byte{137}, MaxPayload)
	if err := left.Send(&wire.Frame{Payload: payload}); err != nil {
		t.Fatal(err)
	}
	got, err := right.Recv()
	if err != nil || !bytes.Equal(got.GetPayload(), payload) {
		t.Fatal("frame corrupted", err)
	}
	if err := right.Send(&wire.Frame{}); err != nil {
		t.Fatal(err)
	}
	if f, err := left.Recv(); err != nil || len(f.Payload) != 0 {
		t.Fatal(err)
	}
	until(t, func() bool {
		s, err := x.directory.Inspect(ctx, &wire.InspectRequest{})
		if err != nil {
			return false
		}
		for _, r := range s.Regions {
			if r.Report.Region == "east" {
				return r.Report.Messages == 2 && r.Report.Bytes == MaxPayload && r.Report.Streams == 2
			}
		}
		return false
	})
	// New generation displaces both old legs, then times out waiting for its peer.
	next := x.route(t, x.a, "worker", "west")
	if next.Generation <= a.Generation {
		t.Fatal("generation reused")
	}
	waiting := openTestStream(t, ctx, next, "worker", ac)
	_, err = left.Recv()
	expectReason(t, err, codes.Aborted, "REPLACED")
	_, err = right.Recv()
	expectReason(t, err, codes.Aborted, "PEER_REPLACED")
	_, err = waiting.Recv()
	expectReason(t, err, codes.Unavailable, "PAIR_TIMEOUT")
}
func TestCloudTenantPolicy(t *testing.T) {
	x := cloudFixture(t, time.Second)
	ctx := t.Context()
	d := wire.NewCloudClient(testConn(t, x.address, x.a))
	_, err := d.Inspect(ctx, &wire.InspectRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	_, err = d.Heartbeat(ctx, &wire.RegionReport{Region: "east", Instance: "wrong"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	_, err = d.Resolve(ctx, &wire.Registration{Pair: "jobs", Target: "mobile"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	_, err = d.Resolve(ctx, &wire.Registration{Pair: "only-east", Target: "worker", Region: "west"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Resolve(ctx, &wire.Registration{Pair: "jobs", Target: "worker"})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatal(err)
	}
	anon, err := dial(Identity{CAFile: x.a.CAFile}, x.address, "cloud.test", false)
	if err != nil {
		t.Fatal(err)
	}
	defer anon.Close()
	external := wire.NewCloudClient(anon)
	in := &wire.Registration{Pair: "jobs", Target: "worker", Region: "east"}
	if _, err := external.Resolve(ctx, in); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	auth := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer external")
	r, err := external.Resolve(auth, in)
	if err != nil || r.Region != "west" {
		t.Fatal(r, err)
	}
	x.regions["east"].blocked.Store(true)
	until(t, func() bool {
		_, err := d.Resolve(ctx, &wire.Registration{Pair: "only-east", Target: "worker"})
		return status.Code(err) == codes.Unavailable
	})
}
func TestJWTAdmission(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pub.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := newCloudAuth([]CloudMember{{Tenant: "one", ID: "alice", Subject: "subject"}}, &JWTPolicy{"issuer", "aud", "connect", path})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		issuer, aud, subject, scope string
		exp                         int64
		code                        codes.Code
	}{
		{"issuer", "aud", "subject", "read connect", 3600, codes.OK}, {"issuer", "aud", "subject", "read", 3600, codes.PermissionDenied}, {"issuer", "aud", "other", "connect", 3600, codes.PermissionDenied}, {"else", "aud", "subject", "connect", 3600, codes.Unauthenticated}, {"issuer", "other", "subject", "connect", 3600, codes.Unauthenticated}, {"issuer", "aud", "subject", "connect", -10, codes.Unauthenticated},
	} {
		token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": tc.issuer, "aud": tc.aud, "sub": tc.subject, "scope": tc.scope, "exp": time.Now().Unix() + tc.exp}).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		ctx := peer.NewContext(t.Context(), &peer.Peer{AuthInfo: credentials.TLSInfo{}})
		id, err := a.member(metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)))
		if status.Code(err) != tc.code || (err == nil && id != (memberID{"one", "alice"})) {
			t.Fatalf("%+v: %v %v", tc, id, err)
		}
	}
}

func TestRouteDiscoveryAndPresence(t *testing.T) {
	x := cloudFixture(t, time.Second)
	x.regions["east"].probeDelay.Store(int64(500 * time.Millisecond))
	c := Client{config: ClientConfig{Identity: x.a, Cloud: &CloudConfig{Pair: "jobs", Target: "worker"}}}
	if err := c.UpdateRegistration([]string{"jobs"}, 27); err != nil {
		t.Fatal(err)
	}
	conn := testConn(t, x.address, x.a)
	route, err := c.resolve(t.Context(), conn)
	if err != nil || route.Region != "west" {
		t.Fatal(route, err)
	}
	reg := c.registration()
	reg.Load = 52
	member, err := wire.NewCloudClient(conn).Register(t.Context(), reg)
	if err != nil || member.Id != "mobile" {
		t.Fatal(member, err)
	}
	state, err := x.directory.Inspect(t.Context(), &wire.InspectRequest{})
	if err != nil || len(state.Hosts) != 1 || !state.Hosts[0].Recent || state.Hosts[0].Registration.Load != 52 {
		t.Fatal(state, err)
	}
	// Refreshing capacity leaves a previously issued single-use ticket valid.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	s := openTestStream(t, ctx, route, "worker", testConn(t, route.Address, x.a))
	_, err = s.Recv()
	expectReason(t, err, codes.Unavailable, "PAIR_TIMEOUT")
}

func TestAuthorityRestart(t *testing.T) {
	cfg := ControlConfig{StateFile: filepath.Join(t.TempDir(), "routes.db"),
		Members: []CloudMember{{Tenant: "one", ID: "a", CertName: "a.test"}, {Tenant: "one", ID: "b", CertName: "b.test"}},
		Regions: []CloudRegion{{"r", "localhost:7443", "cloud.test", "relay.test"}},
		Pairs:   []CloudPair{{"one", "p", []string{"a", "b"}, []string{"r"}}}}
	peerCtx := func(name string) context.Context {
		cert := &x509.Certificate{DNSNames: []string{name}, Raw: []byte(name)}
		return peer.NewContext(t.Context(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}}}})
	}
	const lease = time.Second
	open := func() *Control {
		c, err := newControl(cfg, lease)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	start := func(c *Control) {
		_, err := c.Heartbeat(peerCtx("relay.test"), &wire.RegionReport{Region: "r", Instance: "new"})
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("new authority granted lease during fencing interval: %v", err)
		}
		until(t, func() bool {
			_, err := c.Heartbeat(peerCtx("relay.test"), &wire.RegionReport{Region: "r", Instance: "new"})
			return err == nil
		})
	}
	resolve := func(c *Control, host, target string, previous uint64) *wire.Route {
		t.Helper()
		route, err := c.Resolve(peerCtx(host), &wire.Registration{Pair: "p", Target: target, PreviousGeneration: previous})
		if err != nil {
			t.Fatal(err)
		}
		return route
	}
	claim := func(host, target string, route *wire.Route) *wire.Claim {
		return &wire.Claim{Region: "r", Instance: "new", Pair: "p", Target: target,
			Generation: route.Generation, Ticket: route.Ticket, CertificateHash: certificateHash(peerCtx(host))}
	}
	admit := func(c *Control, ticket *wire.Claim) {
		t.Helper()
		if _, err := c.Admit(peerCtx("relay.test"), ticket); err != nil {
			t.Fatal(err)
		}
	}
	a := open()
	start(a)
	peerRoute := resolve(a, "b.test", "a", 0)
	admit(a, claim("b.test", "a", peerRoute))
	old := resolve(a, "a.test", "b", 0)
	admit(a, claim("a.test", "b", old))
	current := resolve(a, "a.test", "b", 0)
	admit(a, claim("a.test", "b", current))
	// Issuing a later ticket alone must not supersede the current instance.
	unspent := resolve(a, "a.test", "b", 0)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b := open()
	start(b)
	// The old instance missed its replacement trailers and retries first.
	_, err := b.Resolve(peerCtx("a.test"), &wire.Registration{Pair: "p", Target: "b", PreviousGeneration: old.Generation})
	expectReason(t, err, codes.Aborted, "REPLACED")
	_, err = b.Admit(peerCtx("relay.test"), claim("a.test", "b", unspent))
	if status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	fresh := resolve(b, "a.test", "b", current.Generation)
	if fresh.Generation <= unspent.Generation {
		t.Fatal("route generation did not advance across restart")
	}
	admit(b, claim("a.test", "b", fresh))
	peerFresh := resolve(b, "b.test", "a", peerRoute.Generation)
	admit(b, claim("b.test", "a", peerFresh))
	_, err = b.Heartbeat(peerCtx("relay.test"), &wire.RegionReport{Region: "r", Instance: "duplicate"})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatal(err)
	}
}

func TestBearerPairTenantIsolation(t *testing.T) {
	x := cloudFixtureWithCapacity(t, 4*time.Second, 4)
	id := Identity{CAFile: x.a.CAFile}
	cfg := ClientConfig{Identity: id, Address: x.address, ServerName: "cloud.test", Cloud: &CloudConfig{Pair: "jobs", Target: "worker", BearerFile: filepath.Join(t.TempDir(), "token")}}
	if err := os.WriteFile(cfg.Cloud.BearerFile, []byte("external\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cc, err := dial(id, x.address, "cloud.test", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	c := Client{config: cfg}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	a, err := c.resolve(ctx, cc)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := dial(id, a.Address, a.ServerName, false)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	s := openTestStream(t, ctx, a, "worker", rc)
	// Same pair label and reciprocal target, but bound to another tenant.
	b := x.route(t, x.b, "mobile", "west")
	waiting := openTestStream(t, ctx, b, "mobile", testConn(t, b.Address, x.b))
	until(t, func() bool { return x.regions["west"].report().Streams == 2 })
	if err := os.WriteFile(cfg.Cloud.BearerFile, []byte("external-worker"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Cloud.Target = "mobile"
	r, err := c.resolve(ctx, cc)
	if err != nil {
		t.Fatal(err)
	}
	right := openTestStream(t, ctx, r, "mobile", rc)
	acmeRoute := x.route(t, x.a, "worker", "west")
	acme := openTestStream(t, ctx, acmeRoute, "worker", testConn(t, acmeRoute.Address, x.a))
	for _, pair := range []struct {
		left, right wire.CloudRelay_TunnelClient
		payload     string
	}{{s, right, "bearer tenant"}, {acme, waiting, "certificate tenant"}} {
		if h, err := pair.left.Header(); err != nil || len(h.Get(admittedHeader)) != 1 {
			t.Fatal(h, err)
		}
		if err := pair.left.Send(&wire.Frame{Payload: []byte(pair.payload)}); err != nil {
			t.Fatal(err)
		}
		f, err := pair.right.Recv()
		if err != nil || string(f.GetPayload()) != pair.payload {
			t.Fatalf("tenant payload: %q, %v", f.GetPayload(), err)
		}
	}
}

func TestLiveBearerRevocation(t *testing.T) {
	if testing.Short() {
		t.Skip("exercises the production registration refresh interval")
	}
	x := cloudFixture(t, 4*time.Second)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	file := filepath.Join(t.TempDir(), "edge.token")
	if err := os.WriteFile(file, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &Client{row: ctx, config: ClientConfig{Identity: Identity{CAFile: x.a.CAFile}, Address: x.address, ServerName: "cloud.test", Cloud: &CloudConfig{Pair: "jobs", Target: "worker", BearerFile: file}},
		sessions: tcp.Sessions{Open: func(context.Context, tcp.Far) (tcp.Near, error) {
			return tcp.Near{Receive: func(context.Context, []byte) error { return nil }, End: func(context.Context, tcp.CloseReason) error { return nil }}, nil
		}}}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Stop)
	cc, err := dial(Identity{CAFile: x.a.CAFile}, x.address, "cloud.test", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	auth := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer external-worker")
	r, err := wire.NewCloudClient(cc).Resolve(auth, &wire.Registration{Pair: "jobs", Target: "mobile"})
	if err != nil {
		t.Fatal(err)
	}
	regional, err := dial(Identity{CAFile: x.a.CAFile}, r.Address, r.ServerName, false)
	if err != nil {
		t.Fatal(err)
	}
	defer regional.Close()
	leg := openTestStream(t, ctx, r, "mobile", regional)
	if h, err := leg.Header(); err != nil || len(h.Get(admittedHeader)) != 1 {
		t.Fatal(h, err)
	}
	waitClientStatus(t, client, func(s ClientStatus) bool { return s.Phase == "admitted" })
	if err := os.WriteFile(file, []byte("unknown"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := leg.Recv(); err == nil || ctx.Err() != nil {
		t.Fatalf("revocation failed: %v", err)
	}
	s := waitClientStatus(t, client, func(s ClientStatus) bool { return s.Phase == "retrying" })
	if s.LastError != codes.Unauthenticated.String() || time.Until(s.RetryAt) < 25*time.Second {
		t.Fatalf("unexpected denial backoff: %+v", s)
	}
}

func (x *cloudTest) startMember(t *testing.T, id Identity, target string) *Client {
	t.Helper()
	c := &Client{row: t.Context(), config: ClientConfig{Identity: id, Address: x.address, ServerName: "cloud.test", Cloud: &CloudConfig{Pair: "jobs", Target: target, Region: "east"}},
		sessions: tcp.Sessions{Open: func(context.Context, tcp.Far) (tcp.Near, error) {
			return tcp.Near{Receive: func(context.Context, []byte) error { return nil }, End: func(context.Context, tcp.CloseReason) error { return nil }}, nil
		}}}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	return c
}

func TestCloudRollingReplacement(t *testing.T) {
	x := cloudFixture(t, time.Second)
	old := x.startMember(t, x.a, "worker")
	peer := x.startMember(t, x.b, "mobile")
	admitted := func(s ClientStatus) bool { return s.Phase == "admitted" }
	replaced := func(s ClientStatus) bool { return s.Phase == "replaced" && s.Reason == "REPLACED" }
	oldGeneration := waitClientStatus(t, old, admitted).Generation
	peerGeneration := waitClientStatus(t, peer, admitted).Generation
	fresh := x.startMember(t, x.a, "worker")
	waitClientStatus(t, old, replaced)
	waitClientStatus(t, fresh, admitted)
	waitClientStatus(t, peer, func(s ClientStatus) bool { return admitted(s) && s.Generation > peerGeneration })

	// If replacement trailers were lost, resuming the old generation is fenced at Resolve.
	d := wire.NewCloudClient(testConn(t, x.address, x.a))
	_, err := d.Resolve(t.Context(), &wire.Registration{Pair: "jobs", Target: "worker", PreviousGeneration: oldGeneration})
	expectReason(t, err, codes.Aborted, "REPLACED")
	select {
	case <-old.done:
	case <-time.After(time.Second):
		t.Fatal("superseded client is still retrying")
	}
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	waitClientStatus(t, fresh, replaced)
	waitClientStatus(t, old, admitted)
	waitClientStatus(t, peer, admitted)
}

func TestCloudPairTimeoutRetry(t *testing.T) {
	const exhausted = 30 * time.Second
	if d := retryDelay(codes.Unavailable, "PAIR_TIMEOUT", true, exhausted); d != 0 {
		t.Fatalf("late peer would incur a second wait of %v", d)
	}
	if d := retryDelay(codes.Unavailable, "", true, exhausted); d != exhausted {
		t.Fatalf("ordinary outage lost backoff: %v", d)
	}
	x := cloudFixture(t, 200*time.Millisecond)
	first := x.startMember(t, x.a, "worker")
	until(t, func() bool { return x.regions["east"].report().Errors[codes.Unavailable.String()] >= 2 })
	late := x.startMember(t, x.b, "mobile")
	for _, c := range []*Client{first, late} {
		waitClientStatus(t, c, func(s ClientStatus) bool { return s.Phase == "admitted" })
	}
}

func TestSameRelayReacquiresExpiredLease(t *testing.T) {
	x := cloudFixture(t, 5*time.Second)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	a, b := wire.NewCloudClient(testConn(t, x.address, x.a)), wire.NewCloudClient(testConn(t, x.address, x.b))
	reg := &wire.Registration{Pair: "only-east", Target: "worker"}
	left, err := a.Resolve(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := b.Resolve(ctx, &wire.Registration{Pair: "only-east", Target: "mobile"})
	if err != nil {
		t.Fatal(err)
	}
	waiting := openPairStream(t, ctx, left, "only-east", "worker", testConn(t, left.Address, x.a))
	east := x.regions["east"]
	until(t, func() bool { return east.report().Streams == 1 })
	east.blocked.Store(true)
	if _, err := waiting.Recv(); status.Code(err) != codes.Unavailable {
		t.Fatalf("waiting stream survived lease expiry: %v", err)
	}
	// The sole tenant slot is allocated elsewhere while east has no lease.
	until(t, func() bool {
		_, err := a.Resolve(ctx, reg)
		return status.Code(err) == codes.Unavailable
	})
	if r := x.route(t, x.a, "worker", "west"); r.Region != "west" {
		t.Fatal(r)
	}
	east.blocked.Store(false)
	until(t, func() bool { _, err := east.Ping(ctx, &wire.Probe{}); return err == nil })
	if _, err := a.Resolve(ctx, reg); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expired placement bypassed tenant quota: %v", err)
	}
	_, err = openPairStream(t, ctx, ticket, "only-east", "mobile", testConn(t, left.Address, x.b)).Recv()
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ticket from previous lease: %v", err)
	}
}

func TestRegionalPairCapacity(t *testing.T) {
	for _, tc := range []struct {
		name               string
		advertised, actual int
	}{{"reservation", 2, 2}, {"stale heartbeat", 4, 2}, {"one stream left", 4, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			lag := tc.actual != tc.advertised
			x := cloudFixtureWithCapacity(t, 3*time.Second, tc.advertised)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			west := x.regions["west"]
			id := Identity{CAFile: x.a.CAFile}
			anonymous := func(addr string) *grpc.ClientConn {
				t.Helper()
				cc, err := dial(id, addr, "cloud.test", false)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { cc.Close() })
				return cc
			}
			d := wire.NewCloudClient(anonymous(x.address))
			bearer := func(token string) context.Context {
				return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
			}
			reg := &wire.Registration{Pair: "jobs", Target: "worker", Region: "west"}
			leftRoute, err := d.Resolve(bearer("external"), reg)
			if err != nil {
				t.Fatal(err)
			}
			member := wire.NewCloudClient(testConn(t, x.address, x.a))
			candidates, err := member.Candidates(ctx, reg)
			if err != nil {
				t.Fatal(err)
			}
			if !lag && (len(candidates.Regions) != 1 || candidates.Regions[0].Name != "east") {
				t.Fatalf("reservation must cover both legs before either joins: %v", candidates)
			}
			a, b := x.route(t, x.a, "worker", "west"), x.route(t, x.b, "mobile", "west")
			rightRoute, err := d.Resolve(bearer("external-worker"), &wire.Registration{Pair: "jobs", Target: "mobile"})
			if err != nil {
				t.Fatal(err)
			}
			cc := anonymous(leftRoute.Address)
			left, right := openTestStream(t, ctx, leftRoute, "worker", cc), openTestStream(t, ctx, rightRoute, "mobile", cc)
			if h, err := left.Header(); err != nil || len(h.Get(admittedHeader)) != 1 {
				t.Fatal(h, err)
			}

			if lag {
				if a.Region != "west" || b.Region != "west" {
					t.Fatal(a, b)
				}
				// The live limit changed after both tickets were issued from the previous report.
				west.mu.Lock()
				west.maxStreams = tc.actual
				west.mu.Unlock()
				_, err = openTestStream(t, ctx, a, "worker", testConn(t, a.Address, x.a)).Recv()
				expectReason(t, err, codes.ResourceExhausted, "STREAM_QUOTA")
				_, err = openTestStream(t, ctx, b, "mobile", testConn(t, b.Address, x.b)).Recv()
				if status.Code(err) != codes.PermissionDenied {
					t.Fatal("old reciprocal ticket", err)
				}
				a, b = x.route(t, x.a, "worker", "west"), x.route(t, x.b, "mobile", "west")
			}
			if a.Region != "east" || b.Region != "east" {
				t.Fatalf("both members must move to east: %s, %s", a.Region, b.Region)
			}
			otherLeft := openTestStream(t, ctx, a, "worker", testConn(t, a.Address, x.a))
			otherRight := openTestStream(t, ctx, b, "mobile", testConn(t, b.Address, x.b))
			for _, streams := range [][2]wire.CloudRelay_TunnelClient{{otherLeft, otherRight}, {left, right}} {
				if err := streams[0].Send(&wire.Frame{Payload: []byte("online")}); err != nil {
					t.Fatal(err)
				}
				if f, err := streams[1].Recv(); err != nil || string(f.GetPayload()) != "online" {
					t.Fatal(f, err)
				}
			}
		})
	}
}

func TestNoCapacityInAllowedRegion(t *testing.T) {
	x := cloudFixture(t, time.Second)
	x.route(t, x.a, "worker", "west")
	cc, err := dial(Identity{CAFile: x.a.CAFile}, x.address, "cloud.test", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	ctx := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer external")
	_, err = wire.NewCloudClient(cc).Resolve(ctx, &wire.Registration{Pair: "jobs", Target: "worker"})
	expectReason(t, err, codes.ResourceExhausted, "STREAM_QUOTA")
}
