// Package linkssh connects rutis sessions and forwards TCP streams through SSH.
package linkssh

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/arcships/rutis-network/link/tcp"
)

type role int

const (
	dialRole role = iota
	listenRole
	tunnelRole
)

// Credentials are selected by name. Only the deployment supplies local file paths.
type profile struct {
	User         string `json:"user"`
	KnownHosts   string `json:"knownHosts"`
	IdentityFile string `json:"identityFile"`
	CA           string `json:"ca"`
	Cert         string `json:"cert"`
	Key          string `json:"key"`
}

type endpoint struct {
	SSHAddress    string `json:"sshAddress,omitempty"`
	TargetAddress string `json:"targetAddress,omitempty"`
	ListenAddress string `json:"listenAddress,omitempty"`
	LocalTarget   string `json:"localTarget,omitempty"`
	ServerName    string `json:"servername,omitempty"`
	Profile       string `json:"profile,omitempty"`
}

type config struct {
	endpoint
	Profiles       map[string]profile `json:"profiles"`
	ControlService string             `json:"controlService"`
	AutoStart      *bool              `json:"autoStart"`
	Reconnect      *bool              `json:"reconnect"`
}

func (e endpoint) ready(r role) bool {
	if e.SSHAddress == "" || e.Profile == "" {
		return false
	}
	if r == dialRole {
		return e.TargetAddress != ""
	}
	return e.ListenAddress != "" && (r != tunnelRole || e.LocalTarget != "")
}

func (e endpoint) relayAddress() string {
	if e.ListenAddress != "" {
		return e.ListenAddress
	}
	return e.TargetAddress
}

func validAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	n, errPort := strconv.ParseUint(port, 10, 16)
	return err == nil && tcp.ValidHost(host) && errPort == nil && n > 0
}

func (e endpoint) validate(r role, profiles map[string]profile) error {
	if e.SSHAddress != "" && !validAddress(e.SSHAddress) {
		return errors.New("invalid sshAddress")
	}
	if e.TargetAddress != "" && !validAddress(e.TargetAddress) {
		return errors.New("invalid targetAddress")
	}
	if e.ServerName != "" && (!tcp.ValidHost(e.ServerName) || strings.Contains(e.ServerName, "%")) {
		return errors.New("invalid servername")
	}
	if e.ListenAddress != "" {
		host, port, err := net.SplitHostPort(e.ListenAddress)
		_, portErr := strconv.ParseUint(port, 10, 16)
		if r == dialRole || err != nil || portErr != nil || !net.ParseIP(host).IsLoopback() {
			return errors.New("listenAddress must be a relay loopback IP and port (0..65535)")
		}
	}
	if e.LocalTarget != "" && (r != tunnelRole || !validAddress(e.LocalTarget)) {
		return errors.New("invalid localTarget")
	}
	if r != dialRole && (e.TargetAddress != "" || e.ServerName != "") {
		return errors.New("targetAddress and servername are dial settings")
	}
	if _, ok := profiles[e.Profile]; e.Profile != "" && !ok {
		return errors.New("unknown credential profile")
	}
	return nil
}

func (cfg config) validate(r role) error {
	if len(cfg.Profiles) == 0 {
		return errors.New("declare at least one SSH credential profile")
	}
	for name, p := range cfg.Profiles {
		if name == "" || p.User == "" || p.KnownHosts == "" {
			return errors.New("a credential profile requires user and knownHosts")
		}
		if r == tunnelRole && (p.CA != "" || p.Cert != "" || p.Key != "") {
			return errors.New("configure mTLS on the tunnel target")
		}
		if r != tunnelRole && (p.CA == "" || p.Cert == "" || p.Key == "") {
			return errors.New("a rutis SSH link requires ca, cert and key for mTLS")
		}
	}
	if cfg.ControlService == "" && (!cfg.ready(r) || cfg.AutoStart != nil && !*cfg.AutoStart) {
		return errors.New("a static SSH row requires a complete endpoint, profile and automatic start")
	}
	if name := cfg.ControlService; strings.TrimSpace(name) != name || strings.ContainsAny(name, "#\x00\r\n") || strings.HasPrefix(name, "rutis/") {
		return errors.New("invalid controlService")
	}
	return cfg.endpoint.validate(r, cfg.Profiles)
}

type settings struct {
	endpoint
	Reconnect bool `json:"reconnect"`
}

func (s settings) patch(fields map[string]any, r role, profiles map[string]profile) (settings, error) {
	if fields == nil {
		return s, errors.New("configure expects an object")
	}
	for key, value := range fields {
		switch key {
		case "sshAddress", "targetAddress", "listenAddress", "localTarget", "servername", "profile":
			v, ok := value.(string)
			if !(key == "servername" && value == nil) && (!ok || v == "") {
				return s, fmt.Errorf("invalid %s", key)
			}
			switch key {
			case "sshAddress":
				s.SSHAddress = v
			case "targetAddress":
				s.TargetAddress = v
			case "listenAddress":
				s.ListenAddress = v
			case "localTarget":
				s.LocalTarget = v
			case "servername":
				if r != dialRole {
					return s, errors.New("servername is a dial setting")
				}
				s.ServerName = v
			case "profile":
				s.Profile = v
			}
		case "reconnect":
			v, ok := value.(bool)
			if !ok {
				return s, errors.New("invalid reconnect")
			}
			s.Reconnect = v
		default:
			return s, fmt.Errorf("unsupported configuration field %q", key)
		}
	}
	return s, s.endpoint.validate(r, profiles)
}

func schema(r role) map[string]any {
	text := map[string]any{"type": "string", "minLength": 1}
	flag := map[string]any{"type": "boolean"}
	required := []string{"user", "knownHosts"}
	credentialFields := map[string]any{"user": text, "knownHosts": text, "identityFile": text}
	if r != tunnelRole {
		required = append(required, "ca", "cert", "key")
		credentialFields["ca"], credentialFields["cert"], credentialFields["key"] = text, text, text
	}
	credentials := map[string]any{
		"type": "object", "additionalProperties": false, "required": required, "properties": credentialFields,
	}
	fields := map[string]any{
		"profiles":   map[string]any{"type": "object", "minProperties": 1, "additionalProperties": credentials},
		"sshAddress": text, "profile": text, "controlService": text, "autoStart": flag, "reconnect": flag,
	}
	if r == dialRole {
		fields["targetAddress"], fields["servername"] = text, text
	} else {
		fields["listenAddress"] = text
		if r == tunnelRole {
			fields["localTarget"] = text
		}
	}
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"profiles"}, "properties": fields,
	}
}
