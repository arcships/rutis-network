// Package libp2p connects one authenticated libp2p stream to a rutis session.
package libp2p

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

type config struct {
	KeyFile        string `json:"keyFile"`
	Dial           string `json:"dial,omitempty"`
	Listen         string `json:"listen,omitempty"`
	AllowPeer      string `json:"allowPeer,omitempty"`
	Relay          string `json:"relay,omitempty"`
	ControlService string `json:"controlService,omitempty"`
}

func rowSchema() map[string]any {
	variant := func(required []string, optional ...string) map[string]any {
		fields := map[string]any{"controlService": map[string]any{"type": "string"}}
		for _, name := range append(required, optional...) {
			fields[name] = map[string]any{"type": "string"}
		}
		return map[string]any{"type": "object", "properties": fields, "required": required, "additionalProperties": false}
	}
	return map[string]any{"type": "object", "oneOf": []any{
		variant([]string{"keyFile", "dial"}),
		variant([]string{"keyFile", "listen", "allowPeer"}, "relay"),
	}}
}

type settings struct {
	key         crypto.PrivKey
	dial        *peer.AddrInfo
	listen      ma.Multiaddr
	allowed     peer.ID
	relay       *peer.AddrInfo
	circuitAddr string
	circuit     bool
}

func addressedPeer(value string) (*peer.AddrInfo, error) {
	info, err := peer.AddrInfoFromString(value)
	if err != nil || len(info.Addrs) == 0 {
		return nil, errors.New("expected a multiaddr with a transport address and a final /p2p/<peer-id>")
	}
	return info, nil
}

func (c config) parse() (settings, error) {
	var s settings
	if (c.Dial == "") == (c.Listen == "") {
		return s, errors.New("set exactly one of dial and listen")
	}
	if strings.TrimSpace(c.ControlService) != c.ControlService || strings.ContainsAny(c.ControlService, "#\x00\r\n") || strings.HasPrefix(c.ControlService, "rutis/") {
		return s, errors.New("invalid controlService name")
	}
	var err error
	if c.Listen != "" {
		if s.listen, err = ma.NewMultiaddr(c.Listen); err != nil {
			return s, fmt.Errorf("listen: %w", err)
		}
		if s.allowed, err = peer.Decode(c.AllowPeer); err != nil {
			return s, errors.New("listen requires a valid allowPeer")
		}
		if c.Relay != "" {
			if s.relay, err = addressedPeer(c.Relay); err != nil {
				return s, fmt.Errorf("relay: %w", err)
			}
			s.circuitAddr = s.relay.Addrs[0].String() + "/p2p/" + s.relay.ID.String() + "/p2p-circuit"
		}
	} else {
		if c.AllowPeer != "" || c.Relay != "" {
			return s, errors.New("allowPeer and relay are listener settings")
		}
		if s.dial, err = addressedPeer(c.Dial); err != nil {
			return s, fmt.Errorf("dial: %w", err)
		}
		_, err = s.dial.Addrs[0].ValueForProtocol(ma.P_CIRCUIT)
		s.circuit = err == nil
	}
	data, err := os.ReadFile(c.KeyFile)
	if err != nil {
		return s, fmt.Errorf("keyFile: %w", err)
	}
	s.key, err = crypto.UnmarshalPrivateKey(data)
	if err != nil {
		return s, errors.New("keyFile: invalid libp2p private key")
	}
	return s, nil
}

// GenerateKey creates a new libp2p identity without overwriting an existing one.
func GenerateKey(path string) (peer.ID, error) {
	key, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return "", err
	}
	id, err := peer.IDFromPrivateKey(key)
	if err != nil {
		return "", err
	}
	data, err := crypto.MarshalPrivateKey(key)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		os.Remove(path)
		return "", err
	}
	return id, nil
}
