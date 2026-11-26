// Test rows run in a separate Go runtime and use the control service just as
// a local management plugin would. Only the read-only witness is exported.
package main

import (
	"context"
	"errors"
	"strings"

	"github.com/arcships/rutis/go/rutis"
)

type status struct {
	ListenAddrs []string `json:"listenAddrs"`
	RelayReady  bool     `json:"relayReady"`
}
type controls struct {
	Status func(context.Context) (status, error)
	Stop   func(context.Context) error
	Start  func(context.Context) error
}

type witness struct {
	control controls
	relay   bool
}

func (*witness) Echo(msg string) string { return msg }
func (w *witness) Address(ctx context.Context) (string, error) {
	s, err := w.control.Status(ctx)
	if err != nil {
		return "", err
	}
	for _, addr := range s.ListenAddrs {
		if strings.HasSuffix(addr, "/p2p-circuit") == w.relay && (!w.relay || s.RelayReady) {
			return addr, nil
		}
	}
	return "", errors.New("waiting for listener address")
}

type config struct {
	Action string `json:"action,omitempty"`
	Relay  bool   `json:"relay,omitempty"`
}

func main() {
	rutis.Serve(rutis.Define(rutis.Plugin[config]{
		Name: "link-test-probe", Inject: []string{"office-link"},
		Apply: func(ctx *rutis.Ctx, c config) error {
			var control controls
			if err := ctx.Use("office-link", &control); err != nil {
				return err
			}
			switch c.Action {
			case "stop":
				return control.Stop(ctx)
			case "start":
				return control.Start(ctx)
			case "witness":
				ctx.Provide("witness", &witness{control, c.Relay})
				return nil
			default:
				return errors.New("unknown test action")
			}
		},
	}))
}
