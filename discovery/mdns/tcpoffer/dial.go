package tcpoffer

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/arcships/rutis-network/discovery/mdns"
	"github.com/arcships/rutis/go/rutis"
)

type dialConfig struct {
	ID         string   `json:"id"`
	Discovery  string   `json:"discovery"`
	Dialer     string   `json:"dialer"`
	ServerName string   `json:"serverName"`
	AllowCIDRs []string `json:"allowCIDRs"`
}
type dialControl struct {
	Status    func(context.Context) (tcpStatus, error)
	Configure func(context.Context, map[string]any) error
	Start     func(context.Context) error
	Stop      func(context.Context) error
}
type observations struct {
	Snapshot      func(context.Context) (mdns.Snapshot, error)
	WaitForChange func(context.Context, string) (mdns.Snapshot, error)
}

func (cfg dialConfig) target(candidates []mdns.Candidate, allowed []netip.Prefix, now time.Time) (netip.AddrPort, bool) {
	var target netip.AddrPort
	matches := 0
	for _, c := range candidates {
		d := c.Description
		if d == nil || d.ID != cfg.ID || d.Kind != kind || d.Version != 1 {
			continue
		}
		expiry, err := time.Parse(time.RFC3339Nano, c.ExpiresAt)
		if err != nil || !expiry.After(now) {
			continue
		}
		matches++
		var p parameters
		if json.Unmarshal(d.Params, &p) != nil || p.Port == 0 || p.ServerName != "" && p.ServerName != cfg.ServerName {
			continue
		}
		ip, err := netip.ParseAddr(p.Host)
		if err != nil || ip.Zone() != "" {
			continue
		}
		ip = ip.Unmap()
		if ip.IsUnspecified() || ip.IsMulticast() || !slices.ContainsFunc(allowed, func(prefix netip.Prefix) bool { return prefix.Contains(ip) }) {
			continue
		}
		if ip.IsLoopback() {
			iface, err := net.InterfaceByName(c.Interface)
			if err != nil || iface.Flags&net.FlagLoopback == 0 {
				continue
			}
		}
		if ip.Is6() && ip.IsLinkLocalUnicast() {
			ip = ip.WithZone(c.Interface)
		}
		target = netip.AddrPortFrom(ip, p.Port)
	}
	return target, matches == 1 && target.IsValid()
}

func (cfg dialConfig) watch(ctx context.Context, obs observations, dial dialControl, allowed []netip.Prefix) error {
	status, err := dial.Status(ctx)
	if err != nil {
		return err
	}
	if status.Role != "dial" {
		return errors.New("expected a dedicated TCP dial control")
	}
	// The configured dialer belongs to this row. Stop it on loss of its unique offer or the row.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = dial.Stop(cleanup)
	}()
	if err := dial.Stop(ctx); err != nil {
		return err
	}
	var active netip.AddrPort
	for ctx.Err() == nil {
		state, err := obs.Snapshot(ctx)
		for err == nil && ctx.Err() == nil {
			target, ok := cfg.target(state.Candidates, allowed, time.Now())
			switch {
			case !ok && active.IsValid():
				err = dial.Stop(ctx)
				active = netip.AddrPort{}
			case ok && target != active:
				// Write only this connector's public endpoint fields and the locally expected TLS identity.
				err = dial.Configure(ctx, map[string]any{"host": target.Addr().String(), "port": target.Port(), "servername": cfg.ServerName})
				if err == nil {
					err = dial.Start(ctx)
				}
				if err == nil {
					active = target
				}
			}
			if err == nil {
				state, err = obs.WaitForChange(ctx, state.Cursor)
			}
		}
		if ctx.Err() != nil {
			break
		}
		if err = dial.Stop(ctx); err != nil {
			return err
		}
		active = netip.AddrPort{}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return ctx.Err()
}

func dialSchema() map[string]any {
	s := schema([]string{"id", "discovery", "dialer", "serverName", "allowCIDRs"})
	s["properties"].(map[string]any)["allowCIDRs"] = map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}}
	return s
}

var Dial = rutis.Define(rutis.Plugin[dialConfig]{
	Name: "tcp-offer-dial", Schema: dialSchema(),
	Apply: func(row *rutis.Ctx, cfg dialConfig) error {
		if cfg.ServerName == "" || len(cfg.AllowCIDRs) == 0 {
			return errors.New("expected TLS serverName and allowed dial ranges are required")
		}
		var allowed []netip.Prefix
		for _, s := range cfg.AllowCIDRs {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return err
			}
			allowed = append(allowed, p.Masked())
		}
		var obs observations
		var dial dialControl
		if err := row.Use(cfg.Discovery, &obs); err != nil {
			return err
		}
		if err := row.Use(cfg.Dialer, &dial); err != nil {
			return err
		}
		row.Go(func(ctx context.Context) error { return cfg.watch(ctx, obs, dial, allowed) })
		return nil
	},
})
