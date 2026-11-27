package webrtclink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/arcships/rutis/go/rutis"
)

type httpConfig struct {
	Service  string `json:"service"`
	URL      string `json:"url"`
	TokenEnv string `json:"tokenEnv"`
}
type httpProvider struct {
	base, tokenEnv string
	client         *http.Client
}

func (p *httpProvider) call(ctx context.Context, method string, args map[string]any, out any) error {
	token := os.Getenv(p.tokenEnv)
	if token == "" {
		return errors.New("missing signaling credential")
	}
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", p.base+"/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return errSignal
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return &rutis.RemoteError{Name: "PairingDenied", Code: "pairing-denied", Message: "pairing denied"}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return errSignal
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}
func (p *httpProvider) Acquire(ctx context.Context, args map[string]any) (*grant, error) {
	var g *grant
	err := p.call(ctx, "acquire", args, &g)
	return g, err
}
func (p *httpProvider) Publish(ctx context.Context, args map[string]any) error {
	return p.call(ctx, "publish", args, nil)
}
func (p *httpProvider) Receive(ctx context.Context, args map[string]any) (*description, error) {
	var d *description
	err := p.call(ctx, "receive", args, &d)
	return d, err
}

func provideHTTP(row *rutis.Ctx, cfg httpConfig) error {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || cfg.TokenEnv == "" || !validService(cfg.Service) {
		return errors.New("invalid signaling configuration")
	}
	host := u.Hostname()
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "127.0.0.1" || host == "::1" || host == "localhost")) {
		return errors.New("signaling requires HTTPS or loopback")
	}
	p := &httpProvider{
		base: strings.TrimRight(cfg.URL, "/"), tokenEnv: cfg.TokenEnv,
		client: &http.Client{
			Timeout:       5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	row.Provide(cfg.Service, p)
	return nil
}

var HTTPProvider = rutis.Define(rutis.Plugin[httpConfig]{Name: "link-webrtc-signaling", Apply: provideHTTP})
