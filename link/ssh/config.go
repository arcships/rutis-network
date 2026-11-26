// Package linkssh dials a rutis TCP listener through an authenticated SSH channel.
package linkssh

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/arcships/rutis-network/link/tcp"
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

func (e endpoint) ready() bool {
	return e.SSHAddress != "" && e.TargetAddress != "" && e.Profile != ""
}

func validAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	n, errPort := strconv.ParseUint(port, 10, 16)
	return err == nil && tcp.ValidHost(host) && errPort == nil && n > 0
}

func (e endpoint) validate(profiles map[string]profile) error {
	if e.SSHAddress != "" && !validAddress(e.SSHAddress) {
		return errors.New("invalid sshAddress")
	}
	if e.TargetAddress != "" && !validAddress(e.TargetAddress) {
		return errors.New("invalid targetAddress")
	}
	if e.ServerName != "" && (!tcp.ValidHost(e.ServerName) || strings.Contains(e.ServerName, "%")) {
		return errors.New("invalid servername")
	}
	if _, ok := profiles[e.Profile]; e.Profile != "" && !ok {
		return errors.New("unknown credential profile")
	}
	return nil
}

func (cfg config) validate() error {
	if len(cfg.Profiles) == 0 {
		return errors.New("declare at least one SSH credential profile")
	}
	for name, p := range cfg.Profiles {
		if name == "" || p.User == "" || p.KnownHosts == "" || p.CA == "" || p.Cert == "" || p.Key == "" {
			return errors.New("a credential profile requires user, knownHosts, ca, cert and key")
		}
	}
	if cfg.ControlService == "" && (!cfg.ready() || cfg.AutoStart != nil && !*cfg.AutoStart) {
		return errors.New("a static row requires sshAddress, targetAddress, profile and automatic start")
	}
	if name := cfg.ControlService; strings.TrimSpace(name) != name || strings.ContainsAny(name, "#\x00\r\n") || strings.HasPrefix(name, "rutis/") {
		return errors.New("invalid controlService")
	}
	return cfg.endpoint.validate(cfg.Profiles)
}

type settings struct {
	endpoint
	Reconnect bool `json:"reconnect"`
}

func (s settings) patch(fields map[string]any, profiles map[string]profile) (settings, error) {
	if fields == nil {
		return s, errors.New("configure expects an object")
	}
	for key, value := range fields {
		switch key {
		case "sshAddress", "targetAddress", "servername", "profile":
			v, ok := value.(string)
			if !(key == "servername" && value == nil) && (!ok || v == "") {
				return s, fmt.Errorf("invalid %s", key)
			}
			switch key {
			case "sshAddress":
				s.SSHAddress = v
			case "targetAddress":
				s.TargetAddress = v
			case "servername":
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
	return s, s.endpoint.validate(profiles)
}

func schema() map[string]any {
	text := map[string]any{"type": "string", "minLength": 1}
	flag := map[string]any{"type": "boolean"}
	credentials := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"user", "knownHosts", "ca", "cert", "key"},
		"properties": map[string]any{"user": text, "knownHosts": text, "identityFile": text, "ca": text, "cert": text, "key": text},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"profiles"},
		"properties": map[string]any{
			"profiles":   map[string]any{"type": "object", "minProperties": 1, "additionalProperties": credentials},
			"sshAddress": text, "targetAddress": text, "servername": text, "profile": text,
			"controlService": text, "autoStart": flag, "reconnect": flag,
		},
	}
}
