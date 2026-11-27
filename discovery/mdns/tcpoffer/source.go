// Package tcpoffer maps rutis.tcp/v1 descriptions to the managed Go TCP connector.
package tcpoffer

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"

	"github.com/arcships/rutis-network/discovery/mdns"
	"github.com/arcships/rutis/go/rutis"
)

const kind = "rutis.tcp"

type parameters struct {
	Host       string `json:"host"`
	Port       uint16 `json:"port"`
	ServerName string `json:"serverName,omitempty"`
}

type sourceConfig struct {
	Listener   string `json:"listener"`
	Service    string `json:"service"`
	ID         string `json:"id"`
	ServerName string `json:"serverName,omitempty"`
}

type tcpStatus struct {
	Cursor       string `json:"cursor"`
	Role         string `json:"role"`
	Phase        string `json:"phase"`
	LocalAddress string `json:"localAddress"`
}

type source struct {
	cfg      sourceConfig
	listener struct {
		Status        func(context.Context) (tcpStatus, error)
		WaitForChange func(context.Context, string) (tcpStatus, error)
	}
}

func (s *source) describe(status tcpStatus, err error) (mdns.SourceSnapshot, error) {
	if err != nil {
		return mdns.SourceSnapshot{}, err
	}
	if status.Role != "listen" {
		return mdns.SourceSnapshot{}, errors.New("expected TCP listener control")
	}
	state := mdns.SourceSnapshot{Cursor: status.Cursor, Offers: []mdns.Description{}}
	if status.Phase == "listening" {
		ep, err := netip.ParseAddrPort(status.LocalAddress)
		if err != nil || ep.Port() == 0 {
			return mdns.SourceSnapshot{}, errors.New("listener has no bound endpoint")
		}
		// Copy only peer-facing fields. A forwarder supplies its own readiness and endpoint.
		params, err := json.Marshal(parameters{ep.Addr().String(), ep.Port(), s.cfg.ServerName})
		if err != nil {
			return state, err
		}
		state.Offers = append(state.Offers, mdns.Description{ID: s.cfg.ID, Kind: kind, Version: 1, Params: params})
	}
	return state, nil
}
func (s *source) Snapshot(ctx context.Context) (mdns.SourceSnapshot, error) {
	return s.describe(s.listener.Status(ctx))
}
func (s *source) WaitForChange(ctx context.Context, cursor string) (mdns.SourceSnapshot, error) {
	return s.describe(s.listener.WaitForChange(ctx, cursor))
}

func schema(required []string, optional ...string) map[string]any {
	props := map[string]any{}
	for _, k := range append(append([]string(nil), required...), optional...) {
		props[k] = map[string]any{"type": "string", "minLength": 1}
	}
	props["id"] = map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,63}$"}
	return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
}

var Source = rutis.Define(rutis.Plugin[sourceConfig]{
	Name: "tcp-offer-source", Schema: schema([]string{"id", "listener", "service"}, "serverName"),
	Apply: func(row *rutis.Ctx, cfg sourceConfig) error {
		if cfg.Service == cfg.Listener {
			return errors.New("listener and source must have different names")
		}
		s := &source{cfg: cfg}
		if err := row.Use(cfg.Listener, &s.listener); err != nil {
			return err
		}
		row.Provide(cfg.Service, s)
		return nil
	},
})
