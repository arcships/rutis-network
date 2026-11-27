package tcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
)

// A nil port distinguishes an unconfigured managed listener from port zero.
type config struct {
	CA             string `json:"ca"`
	Cert           string `json:"cert"`
	Key            string `json:"key"`
	ControlService string `json:"controlService"`
	// Older local configs injected the manager into the transport itself.
	LegacyReceiver json.RawMessage `json:"controlReceiver"`
	AutoStart      *bool           `json:"autoStart"`
	Host           string          `json:"host"`
	Port           *int            `json:"port"`
	ServerName     string          `json:"servername"`
	Reconnect      *bool           `json:"reconnect"`
}

func schema(dial bool) map[string]any {
	text := map[string]any{"type": "string", "minLength": 1}
	flag := map[string]any{"type": "boolean"}
	props := map[string]any{"ca": text, "cert": text, "key": text, "controlService": text, "controlReceiver": false, "autoStart": flag}
	minPort := 0
	if dial {
		minPort = 1
		props["host"] = text
		props["servername"] = text
		props["reconnect"] = flag
	}
	props["port"] = map[string]any{"type": "integer", "minimum": minPort, "maximum": 65535}
	return map[string]any{
		"type": "object", "properties": props, "required": []string{"ca", "cert", "key"},
		"anyOf": []map[string]any{
			{"required": []string{"port"}},
			{"required": []string{"controlService"}},
		},
	}
}

type target struct {
	Host       string `json:"host,omitempty"`
	Port       *int   `json:"port,omitempty"`
	ServerName string `json:"servername,omitempty"`
}

func (t target) address() string {
	host := t.Host
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(*t.Port))
}

func (t target) tlsName() string {
	if t.ServerName != "" {
		return t.ServerName
	}
	if t.Host == "" {
		return "localhost"
	}
	if ip, err := netip.ParseAddr(t.Host); err == nil {
		return ip.WithZone("").String()
	}
	return t.Host
}

// ValidHost accepts DNS names and IP literals used by the network dialers.
func ValidHost(name string) bool {
	if ip, err := netip.ParseAddr(name); err == nil {
		zone := ip.Zone()
		return !strings.ContainsAny(zone, "%/[]") && strings.IndexFunc(zone, func(r rune) bool {
			return unicode.IsControl(r) || unicode.IsSpace(r)
		}) < 0
	}
	domain := strings.TrimSuffix(name, ".")
	if len(domain) == 0 || len(domain) > 253 {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func (t target) validate(dial bool) error {
	if !dial && (t.Host != "" || t.ServerName != "") {
		return errors.New("listen only accepts port")
	}
	if t.Host != "" && !ValidHost(t.Host) {
		return errors.New("invalid host")
	}
	if t.ServerName != "" && (!ValidHost(t.ServerName) || strings.Contains(t.ServerName, "%")) {
		return errors.New("invalid servername")
	}
	if t.Port != nil && (*t.Port < 0 || *t.Port > 65535 || dial && *t.Port == 0) {
		return errors.New("invalid port")
	}
	return nil
}

type settings struct {
	target
	Reconnect bool `json:"reconnect"`
}

func (t target) same(other target) bool {
	if t.Host != other.Host || t.ServerName != other.ServerName {
		return false
	}
	if t.Port == nil || other.Port == nil {
		return t.Port == other.Port
	}
	return *t.Port == *other.Port
}

// Keep the distinction between an omitted field and an explicitly invalid value.
func (s settings) patch(fields map[string]any, dial bool) (settings, error) {
	if fields == nil {
		return s, errors.New("configure expects an object")
	}
	for key, value := range fields {
		switch key {
		case "host", "servername":
			v, ok := value.(string)
			// Null clears an override; an omitted field keeps its value.
			if !dial || value != nil && (!ok || v == "") {
				return s, fmt.Errorf("invalid %s", key)
			}
			if key == "host" {
				s.Host = v
			} else {
				s.ServerName = v
			}
		case "port":
			// The SDK decodes JSON numbers into float64 across a runtime.
			raw, err := json.Marshal(value)
			var port int
			if err != nil || value == nil || json.Unmarshal(raw, &port) != nil {
				return s, errors.New("invalid port")
			}
			s.Port = &port
		case "reconnect":
			v, ok := value.(bool)
			if !dial || !ok {
				return s, errors.New("invalid reconnect")
			}
			s.Reconnect = v
		default:
			return s, fmt.Errorf("unsupported configuration field %q", key)
		}
	}
	return s, s.target.validate(dial)
}
