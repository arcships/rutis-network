package rcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"time"
)

const (
	maxLimit = 1 << 30
	minBeat  = 100 * time.Millisecond
	maxBeat  = 5 * time.Minute
)

type Credentials struct {
	Address    string    `json:"address"`
	Role       string    `json:"role"`
	Binding    []byte    `json:"binding"`
	PrivateKey []byte    `json:"privateKey"`
	PeerKey    []byte    `json:"peerKey"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// ErrProviderUnavailable marks a temporary credential service failure. Providers
// can wrap it with %w. Timeouts also retry; other errors reject the grant.
const ErrProviderUnavailable failure = "credential-provider-unavailable"

// Provider is called once per connection attempt. It may return a newly issued
// relay URL and rotated peer binding. Keep account operations in the caller.
type Provider func(ctx context.Context, linkID string) (Credentials, error)

type config struct {
	LinkID         string `json:"linkId"`
	CredentialFile string `json:"credentialFile"`
	Listen         string `json:"listen"`
	Cert           string `json:"cert"`
	Key            string `json:"key"`
	MaxMessage     int    `json:"maxMessage"`
	HeartbeatMs    int    `json:"heartbeatMs"`
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,80}$`)

func fileProvider(path string) Provider {
	return func(context.Context, string) (Credentials, error) {
		var c Credentials
		f, err := os.Open(path)
		if err != nil {
			return c, credential
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return c, credential
		}
		b, err := io.ReadAll(io.LimitReader(f, 65537))
		if err != nil || len(b) > 65536 || json.Unmarshal(b, &c) != nil {
			return c, credential
		}
		return c, nil
	}
}

func endpoint(address string) (*url.URL, error) {
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, credential
	}
	if u.Scheme != "wss" && !(u.Scheme == "ws" && net.ParseIP(u.Hostname()).IsLoopback()) {
		return nil, credential
	}
	return u, nil
}

func (c config) normalized() (config, error) {
	if !validID.MatchString(c.LinkID) {
		return c, errors.New("invalid linkId")
	}
	if c.MaxMessage == 0 {
		c.MaxMessage = defaultLimit
	}
	if c.HeartbeatMs == 0 {
		c.HeartbeatMs = 10000
	}
	if c.MaxMessage < 1 || c.MaxMessage > maxLimit || c.HeartbeatMs < int(minBeat.Milliseconds()) || c.HeartbeatMs > int(maxBeat.Milliseconds()) {
		return c, errors.New("invalid RCP message limit or heartbeat interval")
	}
	return c, nil
}

func (d *device) creds(ctx context.Context) (Credentials, error) {
	c, err := d.provide(ctx, d.cfg.LinkID)
	if err != nil {
		if ctx.Err() != nil {
			return Credentials{}, ctx.Err()
		}
		var timeout net.Error
		if errors.Is(err, ErrProviderUnavailable) || (errors.As(err, &timeout) && timeout.Timeout()) {
			return Credentials{}, ErrProviderUnavailable
		}
		return Credentials{}, credential
	}
	if c.ExpiresAt.IsZero() || !time.Now().Before(c.ExpiresAt) {
		return Credentials{}, expired
	}
	if len(c.PrivateKey) != 32 || len(c.PeerKey) != 32 || len(c.Binding) == 0 || len(c.Binding) > 1024 || (c.Role != "client" && c.Role != "daemon") {
		return Credentials{}, credential
	}
	return c, nil
}
