package mdns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/arcships/rutis/go/rutis"
)

type config struct {
	Service   string       `json:"service"`
	Interface string       `json:"interface"`
	Domain    string       `json:"domain"`
	Browse    string       `json:"browse"`
	Offers    *offerConfig `json:"offers"`
	Publish   *advert      `json:"publish"`
}

type publication struct {
	run    func(context.Context) error
	update func([]byte) error
}

type advert struct {
	txt      []byte
	Type     string `json:"type"`
	Instance string `json:"instance"`
	Port     int    `json:"port"`
}

var serviceType = regexp.MustCompile(`^_([a-z0-9]([a-z0-9-]{0,13}[a-z0-9])?)\._(tcp|udp)$`)

func validType(s string) bool {
	match := serviceType.FindStringSubmatch(s)
	return match != nil && !strings.Contains(match[1], "--") && strings.ContainsAny(match[1], "abcdefghijklmnopqrstuvwxyz")
}

func (c *config) validate() error {
	if c.Domain == "" || strings.EqualFold(c.Domain, "local") || strings.EqualFold(c.Domain, "local.") {
		c.Domain = "local."
	} else {
		return errors.New("domain must be local. for multicast DNS")
	}
	if c.Interface == "" {
		return errors.New("interface is required")
	}
	if c.Offers != nil {
		return c.validateOffers()
	}
	if c.Browse == "" && c.Publish == nil {
		return errors.New("set browse or publish")
	}
	if c.Browse != "" {
		if !validType(c.Browse) {
			return errors.New("browse must be a DNS-SD service type such as _rutis._tcp")
		}
		if !validName(c.Service) {
			return errors.New("browse requires a valid local service name")
		}
	} else if c.Service != "" {
		return errors.New("service requires browse")
	}
	if p := c.Publish; p != nil {
		if !validType(p.Type) || p.Port < 1 || p.Port > 65535 {
			return errors.New("publish requires a valid DNS-SD type and port in 1..65535")
		}
		if len(p.Instance) == 0 || len(p.Instance) > 63 || !utf8.ValidString(p.Instance) || strings.IndexFunc(p.Instance, unicode.IsControl) >= 0 {
			return errors.New("publish.instance must be a 1..63 byte UTF-8 label without control characters")
		}
	}
	return nil
}

func schema() map[string]any {
	text := map[string]any{"type": "string", "minLength": 1}
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"interface"},
		"anyOf": []any{map[string]any{"required": []string{"browse", "service"}}, map[string]any{"required": []string{"publish"}}, map[string]any{"required": []string{"offers"}}},
		"properties": map[string]any{
			"offers": offerSchema(), "interface": text, "service": text, "domain": text, "browse": text,
			"publish": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"type", "instance", "port"},
				"properties": map[string]any{"type": text, "instance": text, "port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535}},
			},
		},
	}
}

// The v0.23 SDK has no public accessor for the configured row ID. Keep the
// single version-dependent lookup here; never infer provenance from DNS data.
func sourceRow(ctx *rutis.Ctx) (string, error) {
	row := reflect.ValueOf(ctx).Elem().FieldByName("row")
	if row.Kind() == reflect.Pointer && !row.IsNil() && row.Elem().Kind() == reflect.Struct {
		name := row.Elem().FieldByName("name")
		if name.Kind() == reflect.String && name.String() != "" {
			return name.String(), nil
		}
	}
	return "", errors.New("could not read the discovery row ID from the rutis SDK")
}

var Plugin = rutis.Define(rutis.Plugin[config]{
	Name: "discover-mdns", Schema: schema(), Apply: apply,
})

func apply(row *rutis.Ctx, cfg config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	iface, err := net.InterfaceByName(cfg.Interface)
	if err != nil {
		return err
	}
	if iface.Flags&(net.FlagUp|net.FlagMulticast) != net.FlagUp|net.FlagMulticast {
		return fmt.Errorf("interface %s must be up and support multicast", cfg.Interface)
	}
	if cfg.Offers != nil && cfg.Offers.Source != "" {
		if err := publishSource(row, cfg.Offers, iface); err != nil {
			return err
		}
	}
	if cfg.Publish != nil {
		run, err := startPublisher(row, *cfg.Publish, iface.Index)
		if err != nil {
			return err
		}
		row.Go(run.run)
	}
	if cfg.Browse != "" {
		source, err := sourceRow(row)
		if err != nil {
			return err
		}
		svc := newObservations(row)
		b, err := openBrowser(iface)
		if err != nil {
			return err
		}
		row.Go(func(ctx context.Context) error {
			err := b.run(ctx, newCache(source, cfg), svc)
			if err != nil && ctx.Err() == nil {
				svc.fail(err)
			}
			return err
		})
		row.Provide(cfg.Service, svc)
	}
	return nil
}
