package webrtclink

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/arcships/rutis/go/rutis"
	rtc "github.com/pion/webrtc/v4"
)

var errGrant = errors.New("invalid-grant")
var errTurn = errors.New("turn-unavailable")
var errDenied = errors.New("pairing-denied")
var errSignal = errors.New("signaling-failed")

type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}
type grant struct {
	AttemptID  string      `json:"attemptId"`
	Role       string      `json:"role"`
	ExpiresAt  string      `json:"expiresAt"`
	IceServers []iceServer `json:"iceServers"`
}
type description struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}
type signaling struct {
	Acquire func(context.Context, map[string]any) (*grant, error)
	Publish func(context.Context, map[string]any) error
	Receive func(context.Context, map[string]any) (*description, error)
}

func signalError(err error) error {
	var remote *rutis.RemoteError
	if errors.Is(err, errDenied) {
		return errDenied
	}
	if errors.As(err, &remote) && (remote.Name == "PairingDenied" || remote.Code == "pairing-denied") {
		return errDenied
	}
	return errSignal
}
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.C:
		return nil
	}
}
func (c *control) acquire(ctx context.Context, pair, after string) (*grant, error) {
	var previous any
	if after != "" {
		previous = after
	}
	for ctx.Err() == nil {
		g, err := c.signal.Acquire(ctx, map[string]any{"pairId": pair, "after": previous, "waitMs": 1000})
		if err != nil {
			return nil, signalError(err)
		}
		if g != nil {
			if g.AttemptID == "" || g.AttemptID == after || g.Role != "offerer" && g.Role != "answerer" {
				return nil, errGrant
			}
			return g, nil
		}
		if err = sleep(ctx, 50*time.Millisecond); err != nil {
			return nil, err
		}
	}
	return nil, context.Cause(ctx)
}

func rtcConfig(g *grant, policy string) (rtc.Configuration, error) {
	cfg := rtc.Configuration{}
	haveTURN := false
	for _, s := range g.IceServers {
		for _, u := range s.URLs {
			if strings.HasPrefix(u, "turn:") || strings.HasPrefix(u, "turns:") {
				haveTURN = true
			}
		}
		cfg.ICEServers = append(cfg.ICEServers, rtc.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	if policy == "relay" {
		if !haveTURN {
			return cfg, errTurn
		}
		cfg.ICETransportPolicy = rtc.ICETransportPolicyRelay
	}
	return cfg, nil
}

func (c *control) exchange(ctx context.Context, pc *rtc.PeerConnection, opts settings, g *grant) error {
	publish := func(desc rtc.SessionDescription) error {
		gathered := rtc.GatheringCompletePromise(pc)
		if err := pc.SetLocalDescription(desc); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-gathered:
		}
		local := pc.LocalDescription()
		if opts.Policy == "relay" && !hasRelayCandidate(local.SDP) {
			return errTurn
		}
		if err := c.signal.Publish(ctx, map[string]any{"pairId": opts.PairID, "attemptId": g.AttemptID, "type": local.Type.String(), "sdp": local.SDP}); err != nil {
			return signalError(err)
		}
		return nil
	}
	receive := func(kind rtc.SDPType) error {
		for ctx.Err() == nil {
			desc, err := c.signal.Receive(ctx, map[string]any{"pairId": opts.PairID, "attemptId": g.AttemptID, "type": kind.String(), "waitMs": 1000})
			if err != nil {
				return signalError(err)
			}
			if desc != nil {
				if desc.Type != kind.String() || desc.SDP == "" {
					return errGrant
				}
				return pc.SetRemoteDescription(rtc.SessionDescription{Type: kind, SDP: desc.SDP})
			}
			if err = sleep(ctx, 50*time.Millisecond); err != nil {
				return err
			}
		}
		return context.Cause(ctx)
	}
	if g.Role == "offerer" {
		offer, err := pc.CreateOffer(nil)
		if err != nil {
			return err
		}
		if err = publish(offer); err != nil {
			return err
		}
		return receive(rtc.SDPTypeAnswer)
	}
	if err := receive(rtc.SDPTypeOffer); err != nil {
		return err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return err
	}
	return publish(answer)
}

func hasRelayCandidate(sdp string) bool {
	for line := range strings.SplitSeq(sdp, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 8 && strings.HasPrefix(fields[0], "a=candidate:") && fields[6] == "typ" && fields[7] == "relay" {
			return true
		}
	}
	return false
}

func route(pc *rtc.PeerConnection) string {
	pair, err := pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local == nil || pair.Remote == nil {
		return "unknown"
	}
	if pair.Local.Typ == rtc.ICECandidateTypeRelay || pair.Remote.Typ == rtc.ICECandidateTypeRelay {
		return "relay"
	}
	return "direct"
}

func (c *control) connect(run context.Context, opts settings, g *grant) error {
	expiry, err := time.Parse(time.RFC3339Nano, g.ExpiresAt)
	if err != nil || !expiry.After(time.Now()) {
		return errGrant
	}
	cfg, err := rtcConfig(g, opts.Policy)
	if err != nil {
		return err
	}
	var engine rtc.SettingEngine
	engine.SetICETimeouts(5*time.Second, 10*time.Second, 2*time.Second)
	pc, err := rtc.NewAPI(rtc.WithSettingEngine(engine)).NewPeerConnection(cfg)
	if err != nil {
		return err
	}
	defer pc.Close()
	ctx, cancel := context.WithCancelCause(run)
	defer cancel(errClosed)
	setup, finish := context.WithDeadline(ctx, expiry)
	defer finish()
	opened := make(chan *wire, 1)
	var attached atomic.Bool
	attach := func(dc *rtc.DataChannel) {
		if !attached.CompareAndSwap(false, true) || dc.Label() != "rutis" || dc.Protocol() != "rutis-link/1" || !dc.Ordered() || dc.MaxRetransmits() != nil || dc.MaxPacketLifeTime() != nil {
			cancel(errProtocol)
			return
		}
		w := newWire(ctx, cancel, dc)
		dc.OnOpen(func() {
			select {
			case opened <- w:
			case <-ctx.Done():
			}
		})
	}
	pc.OnConnectionStateChange(func(s rtc.PeerConnectionState) {
		if s == rtc.PeerConnectionStateFailed || s == rtc.PeerConnectionStateClosed {
			cancel(errClosed)
		}
	})
	pc.OnDataChannel(func(dc *rtc.DataChannel) {
		if g.Role != "answerer" {
			cancel(errProtocol)
			return
		}
		attach(dc)
	})
	if g.Role == "offerer" {
		protocol := "rutis-link/1"
		dc, err := pc.CreateDataChannel("rutis", &rtc.DataChannelInit{Protocol: &protocol})
		if err != nil {
			return err
		}
		attach(dc)
	}
	if err = c.exchange(setup, pc, opts, g); err != nil {
		if errors.Is(setup.Err(), context.DeadlineExceeded) {
			return errGrant
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return err
	}
	select {
	case <-setup.Done():
		if errors.Is(setup.Err(), context.DeadlineExceeded) {
			return errGrant
		}
		return context.Cause(setup)
	case w := <-opened:
		if setup.Err() != nil || !time.Now().Before(expiry) {
			return errGrant
		}
		finish()
		limit := recordLimit
		if maxSize := pc.SCTP().GetCapabilities().MaxMessageSize; maxSize > 0 && maxSize < uint32(limit) {
			limit = int(maxSize)
		}
		if limit < 6 {
			return errProtocol
		}
		return w.serve(c.row, c.sessions, limit, func() {
			r := route(pc)
			c.update(func(s *snapshot) { s.Phase = "connected"; s.Route = r; s.LastError = "" })
			// Only publish samples while this attempt is live.
			go func() {
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						r := route(pc)
						c.update(func(s *snapshot) {
							if ctx.Err() == nil && s.Phase == "connected" {
								s.Route = r
							}
						})
					}
				}
			}()
		})
	}
}
