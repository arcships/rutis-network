package mdns

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

type offerConfig struct {
	Source     string           `json:"source"`
	ListenIP   string           `json:"listenIP"`
	Accept     map[string][]int `json:"accept"`
	AllowCIDRs []string         `json:"allowCIDRs"`
	allowed    []netip.Prefix
}

func offerSchema() map[string]any {
	text := map[string]any{"type": "string", "minLength": 1}
	return map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"source": text, "listenIP": text,
		"accept":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "integer", "minimum": 1, "maximum": 2147483647}}},
		"allowCIDRs": map[string]any{"type": "array", "items": text},
	}}
}

func validName(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "#\x00\r\n") && !strings.HasPrefix(s, "rutis/")
}

func (c *config) validateOffers() error {
	o := c.Offers
	if c.Publish != nil || c.Browse != "" {
		return errors.New("use separate rows for ordinary DNS-SD and connection descriptions")
	}
	if o.Source == "" && len(o.Accept) == 0 {
		return errors.New("offers requires source or accept")
	}
	if o.Source != "" && (!validName(o.Source) || o.Source == c.Service) {
		return errors.New("invalid or conflicting offer source service")
	}
	if o.ListenIP != "" && o.Source == "" {
		return errors.New("listenIP requires an offer source")
	}
	if len(o.Accept) > 0 {
		if !validName(c.Service) {
			return errors.New("accept requires a named discovery service")
		}
		c.Browse = offerType
	} else if c.Service != "" || len(o.AllowCIDRs) > 0 {
		return errors.New("service and allowCIDRs require accept")
	}
	for kind, vs := range o.Accept {
		if !offerKind.MatchString(kind) || len(vs) == 0 {
			return fmt.Errorf("invalid accepted kind %q", kind)
		}
		for _, v := range vs {
			if v < 1 || v > 2147483647 {
				return errors.New("invalid accepted version")
			}
		}
	}
	for _, cidr := range o.AllowCIDRs {
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			return fmt.Errorf("allowCIDRs: %w", err)
		}
		o.allowed = append(o.allowed, p.Masked())
	}
	return nil
}
