// Test rows run in a separate Go runtime and use the control service just as
// a local management plugin would. Only the read-only witness is exported.
package main

import (
	"context"
	"errors"

	"github.com/arcships/rutis/go/rutis"
)

type status struct {
	ListenAddrs []string `json:"listenAddrs"`
}
type controls struct {
	Status func(context.Context) (status, error)
	Stop   func(context.Context) error
	Start  func(context.Context) error
}

type witness struct{ control controls }

func (*witness) Echo(msg string) string { return msg }
func (w *witness) Address(ctx context.Context) (string, error) {
	s, err := w.control.Status(ctx)
	if err != nil {
		return "", err
	}
	if len(s.ListenAddrs) == 0 {
		return "", errors.New("listener is starting")
	}
	return s.ListenAddrs[0], nil
}

type config struct {
	Action string `json:"action,omitempty"`
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
				ctx.Provide("witness", &witness{control})
				return nil
			default:
				return errors.New("unknown test action")
			}
		},
	}))
}
