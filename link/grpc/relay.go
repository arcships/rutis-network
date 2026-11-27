package grpclink

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arcships/rutis-network/link/grpc/wire"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const pairHeader = "x-rutis-pair"
const targetHeader = "x-rutis-target"
const generationHeader = "x-rutis-generation"
const ticketHeader = "x-rutis-ticket"
const versionHeader = "x-rutis-version"

type rendezvous struct {
	key      pairKey
	ctx      context.Context
	cancel   context.CancelCauseFunc
	ready    chan struct{}
	admitted chan struct{}
	headers  atomic.Int32
	legs     map[string]wire.CloudRelay_TunnelServer
}
type memberSlot struct {
	pairKey
	member string
}

type Relay struct {
	wire.UnimplementedCloudRelayServer
	directory        wire.CloudClient
	region, instance string
	pairWait         time.Duration
	maxStreams       int
	mu               sync.Mutex
	until            time.Time
	epoch            uint64
	timer            *time.Timer
	pairs            map[pairKey]*rendezvous
	latest           map[memberSlot]uint64
	admitting        int
	streams          int
	errors           map[string]uint64
	messages, bytes  atomic.Uint64
}

func NewRelay(control wire.CloudClient, region string, maxStreams int, pairWait time.Duration) (*Relay, error) {
	if control == nil || !label(region) || maxStreams < 2 || pairWait <= 0 {
		return nil, errors.New("invalid relay configuration")
	}
	return &Relay{
		directory: control, region: region, instance: randomTicket(), pairWait: pairWait, maxStreams: maxStreams,
		pairs: map[pairKey]*rendezvous{}, latest: map[memberSlot]uint64{}, errors: map[string]uint64{},
	}, nil
}
func (r *Relay) expireLocked() {
	r.until = time.Time{}
	r.epoch++
	for _, p := range r.pairs {
		p.cancel(status.Error(codes.Unavailable, "regional lease ended"))
	}
	clear(r.pairs)
	r.streams = 0
	clear(r.latest)
}
func (r *Relay) report() *wire.RegionReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := &wire.RegionReport{Region: r.region, Instance: r.instance, Messages: r.messages.Load(), Bytes: r.bytes.Load(), Streams: uint32(r.streams), Errors: map[string]uint64{}}
	for k := range r.pairs {
		out.Pairs = append(out.Pairs, &wire.PairKey{Tenant: k.tenant, Pair: k.pair})
	}
	for code, n := range r.errors {
		out.Errors[code] = n
	}
	return out
}

// Run renews the regional lease until cancellation. The independent lease
// timer also fences sessions when a control request or network path stalls.
func (r *Relay) Run(ctx context.Context) {
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.timer != nil {
			r.timer.Stop()
		}
		r.expireLocked()
	}()
	for ctx.Err() == nil {
		start := time.Now()
		call, cancel := context.WithTimeout(ctx, 3*time.Second)
		g, err := r.directory.Heartbeat(call, r.report())
		cancel()
		wait := 250 * time.Millisecond
		if err == nil {
			d := time.Duration(g.Milliseconds) * time.Millisecond
			deadline := start.Add(d)
			r.mu.Lock()
			if !time.Now().Before(r.until) {
				r.expireLocked()
			}
			if time.Now().Before(deadline) {
				r.until = deadline
				if r.timer != nil {
					r.timer.Stop()
				}
				r.timer = time.AfterFunc(time.Until(deadline), func() {
					r.mu.Lock()
					defer r.mu.Unlock()
					if r.until == deadline {
						r.expireLocked()
					}
				})
				wait = d / 3
			}
			r.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
func (r *Relay) Ping(context.Context, *wire.Probe) (*wire.Probe, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !time.Now().Before(r.until) {
		return nil, status.Error(codes.Unavailable, "regional lease unavailable")
	}
	return &wire.Probe{}, nil
}

func one(ctx context.Context, key string) string {
	values := metadata.ValueFromIncomingContext(ctx, key)
	if len(values) != 1 {
		return ""
	}
	return values[0]
}
func (r *Relay) Tunnel(s wire.CloudRelay_TunnelServer) (result error) {
	ctx := s.Context()
	defer func() {
		if result != nil {
			r.mu.Lock()
			r.errors[status.Code(result).String()]++
			r.mu.Unlock()
		}
	}()
	if _, ok := verifiedTLS(ctx); !ok {
		return status.Error(codes.Unauthenticated, "TLS required")
	}
	if one(ctx, versionHeader) != "1" {
		return status.Error(codes.Unimplemented, "unsupported relay protocol")
	}
	pair, target, ticket := one(ctx, pairHeader), one(ctx, targetHeader), one(ctx, ticketHeader)
	gen, err := strconv.ParseUint(one(ctx, generationHeader), 10, 64)
	if !label(pair) || !label(target) || len(ticket) != 64 || err != nil || gen == 0 {
		return status.Error(codes.InvalidArgument, "invalid pairing metadata")
	}
	claim := &wire.Claim{Region: r.region, Instance: r.instance, Pair: pair, Target: target,
		Generation: gen, Ticket: ticket, CertificateHash: certificateHash(ctx)}
	p, err := r.join(s, claim)
	if err != nil {
		return err
	}

	defer func() {
		cause := result
		if cause == nil {
			cause = io.EOF
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.removeLocked(p.key, p, cause)
	}()
	t := time.NewTimer(r.pairWait)
	defer t.Stop()
	select {
	case <-p.ready:
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	case <-p.ctx.Done():
		return context.Cause(p.ctx)
	case <-t.C:
		return pairError(codes.Unavailable, "PAIR_TIMEOUT", pair, target, gen)
	}
	// After ready closes legs is immutable. Each handler owns exactly one
	// read direction and one writer. No application frame queue is needed.
	other := p.legs[target]
	if other == nil {
		return status.Error(codes.Internal, "pair target missing")
	}
	if err := s.SendHeader(metadata.Pairs(admittedHeader, "1")); err != nil {
		return err
	}
	// A peer must send its admission headers before any forwarded DATA.
	if p.headers.Add(1) == 2 {
		close(p.admitted)
	}
	select {
	case <-p.admitted:
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	case <-p.ctx.Done():
		return context.Cause(p.ctx)
	}
	// The gRPC handler must be free to return to unblock a flow-controlled
	// Send or Recv when either transport closes.
	done := make(chan error, 1)
	go func() {
		for p.ctx.Err() == nil {
			f, err := s.Recv()
			if err != nil {
				done <- err
				return
			}
			if len(f.Payload) > MaxPayload {
				done <- status.Error(codes.ResourceExhausted, "frame-too-large")
				return
			}
			if err := other.Send(f); err != nil {
				done <- err
				return
			}
			r.messages.Add(1)
			r.bytes.Add(uint64(len(f.Payload)))
		}
		done <- context.Cause(p.ctx)
	}()
	select {
	case err = <-done:
	case <-ctx.Done():
		err = status.FromContextError(ctx.Err()).Err()
	case <-p.ctx.Done():
		err = context.Cause(p.ctx)
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (r *Relay) removeLocked(k pairKey, p *rendezvous, cause error) {
	p.cancel(cause)
	if r.pairs[k] == p {
		r.streams -= len(p.legs)
		delete(r.pairs, k)
	}
}

// join reserves capacity for an admission RPC before attaching its verified
// member to the current generation. Never hold mu across the directory call.
func (r *Relay) join(s wire.CloudRelay_TunnelServer, claim *wire.Claim) (*rendezvous, error) {
	pair, target, gen := claim.Pair, claim.Target, claim.Generation
	r.mu.Lock()
	if !time.Now().Before(r.until) {
		r.mu.Unlock()
		return nil, status.Error(codes.Unavailable, "regional lease unavailable")
	}
	if r.admitting >= r.maxStreams {
		r.mu.Unlock()
		return nil, pairError(codes.ResourceExhausted, "STREAM_QUOTA", pair, target, gen)
	}
	epoch := r.epoch
	r.admitting++
	r.mu.Unlock()
	call, cancel := context.WithTimeout(s.Context(), 3*time.Second)
	m, err := r.directory.Admit(call, claim)
	cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.admitting--
	if err != nil {
		return nil, err
	}
	k := pairKey{m.Tenant, pair}
	slot := memberSlot{k, m.Id}
	if epoch != r.epoch || !time.Now().Before(r.until) {
		return nil, status.Error(codes.Unavailable, "regional lease changed")
	}
	if gen <= r.latest[slot] {
		return nil, pairError(codes.Aborted, "STALE_GENERATION", pair, target, gen)
	}
	p := r.pairs[k]
	if p != nil && p.legs[m.Id] != nil {
		r.removeLocked(k, p, pairError(codes.Aborted, "REPLACED", pair, target, gen))
		p = nil
	}
	if r.streams >= r.maxStreams {
		return nil, pairError(codes.ResourceExhausted, "STREAM_QUOTA", pair, target, gen)
	}
	r.latest[slot] = gen
	r.streams++
	if p == nil {
		run, stop := context.WithCancelCause(context.Background())
		p = &rendezvous{key: k, ctx: run, cancel: stop, ready: make(chan struct{}), admitted: make(chan struct{}), legs: map[string]wire.CloudRelay_TunnelServer{}}
		r.pairs[k] = p
	}
	p.legs[m.Id] = s
	if len(p.legs) == 2 {
		close(p.ready)
	}
	return p, nil
}
