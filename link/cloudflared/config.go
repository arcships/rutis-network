// Package cloudflared manages a named Cloudflare tunnel or a local Access TCP forwarder.
package cloudflared

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/arcships/rutis/go/rutis"
)

type profile struct {
	Hostname         string `json:"hostname"`
	ServerName       string `json:"servername"`
	ClientIDFile     string `json:"clientIdFile"`
	ClientSecretFile string `json:"clientSecretFile"`
}

type config struct {
	Role            string             `json:"role"`
	Binary          string             `json:"binary"`
	ControlService  string             `json:"controlService"`
	TCPService      string             `json:"tcpService"`
	AutoStart       *bool              `json:"autoStart"`
	ForwardPort     *int               `json:"forwardPort"`
	OriginPort      *int               `json:"originPort"`
	MetricsPort     *int               `json:"metricsPort"`
	TunnelTokenFile string             `json:"tunnelTokenFile"`
	Profile         string             `json:"profile"`
	Profiles        map[string]profile `json:"profiles"`
}

var dnsLabel = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func validDNS(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !dnsLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func validPort(p *int, min int) bool {
	return p != nil && *p >= min && *p <= 65535
}

func (cfg config) validate() error {
	for _, service := range []string{cfg.ControlService, cfg.TCPService} {
		if strings.TrimSpace(service) != service || strings.ContainsAny(service, "#\x00\r\n") || strings.HasPrefix(service, "rutis/") {
			return errors.New("invalid service name")
		}
	}
	if cfg.TCPService != "" && cfg.TCPService == cfg.ControlService {
		return errors.New("controlService and tcpService must differ")
	}
	switch cfg.Role {
	case "dial":
		if !validPort(cfg.ForwardPort, 0) || cfg.MetricsPort != nil || cfg.OriginPort != nil || cfg.TunnelTokenFile != "" {
			return errors.New("dial requires forwardPort (0..65535) and dial settings only")
		}
		if _, ok := cfg.Profiles[cfg.Profile]; !ok || cfg.Profile == "" {
			return errors.New("profile must select a declared target")
		}
		for name, p := range cfg.Profiles {
			if name == "" || !validDNS(p.Hostname) || p.ClientIDFile == "" || p.ClientSecretFile == "" {
				return errors.New("each profile requires hostname, clientIdFile and clientSecretFile")
			}
			if (cfg.TCPService != "" || p.ServerName != "") && !validDNS(p.ServerName) {
				return errors.New("invalid origin certificate servername")
			}
		}
	case "connector":
		if cfg.TunnelTokenFile == "" || cfg.ForwardPort != nil || cfg.Profile != "" || cfg.Profiles != nil {
			return errors.New("connector requires tunnelTokenFile and connector settings only")
		}
		if (cfg.TCPService != "" || cfg.OriginPort != nil) && !validPort(cfg.OriginPort, 1) {
			return errors.New("invalid originPort")
		}
		if cfg.MetricsPort != nil && !validPort(cfg.MetricsPort, 1) {
			return errors.New("invalid metricsPort")
		}
		if cfg.MetricsPort != nil && cfg.OriginPort != nil && *cfg.MetricsPort == *cfg.OriginPort {
			return errors.New("metricsPort and originPort must differ")
		}
	default:
		return errors.New("role must be dial or connector")
	}
	if cfg.ControlService == "" && cfg.AutoStart != nil && !*cfg.AutoStart {
		return errors.New("manual start requires controlService")
	}
	return nil
}

func configSchema() map[string]any {
	text := map[string]any{"type": "string", "minLength": 1}
	port := map[string]any{"type": "integer", "minimum": 1, "maximum": 65535}
	profile := map[string]any{
		"type": "object", "additionalProperties": false,
		"required":   []string{"hostname", "clientIdFile", "clientSecretFile"},
		"properties": map[string]any{"hostname": text, "servername": text, "clientIdFile": text, "clientSecretFile": text},
	}
	return map[string]any{
		"type": "object", "required": []string{"role"}, "additionalProperties": false,
		"properties": map[string]any{
			"role":   map[string]any{"enum": []string{"dial", "connector"}},
			"binary": text, "controlService": text, "tcpService": text, "autoStart": map[string]any{"type": "boolean"},
			"forwardPort": map[string]any{"type": "integer", "minimum": 0, "maximum": 65535},
			"originPort":  port, "metricsPort": port, "tunnelTokenFile": text, "profile": text,
			"profiles": map[string]any{"type": "object", "additionalProperties": profile},
		},
	}
}

// The deployment row declares its instance-specific tcpService in inject.
var Plugin = rutis.Define(rutis.Plugin[config]{
	Name: "link-cloudflared", Schema: configSchema(),
	Apply: func(row *rutis.Ctx, cfg config) error {
		if err := cfg.validate(); err != nil {
			return err
		}
		var tcp *tcpControl
		if cfg.TCPService != "" {
			tcp = new(tcpControl)
			if err := row.Use(cfg.TCPService, tcp); err != nil {
				return err
			}
		}
		c := newControl(row, cfg, tcp)
		row.Effect(func(ctx context.Context) error { return c.stop(ctx) })
		if cfg.ControlService != "" {
			row.Provide(cfg.ControlService, c)
		}
		if cfg.AutoStart == nil || *cfg.AutoStart {
			return c.Start()
		}
		return nil
	},
})
