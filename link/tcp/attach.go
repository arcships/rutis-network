package tcp

import (
	"context"
	"errors"

	"github.com/arcships/rutis/go/rutis"
)

type attachConfig struct {
	ControlService  string `json:"controlService"`
	ControlReceiver string `json:"controlReceiver"`
}

// Attach delivers this instance's control object whenever the transport and
// its manager are both present. Only this row depends on the receiver.
var Attach = rutis.Define(rutis.Plugin[attachConfig]{
	Name: "link-tcp-attach",
	Apply: func(row *rutis.Ctx, cfg attachConfig) error {
		if cfg.ControlService == "" || !validServiceName(cfg.ControlService) {
			return errors.New("invalid controlService name")
		}
		if cfg.ControlReceiver == "" || !validServiceName(cfg.ControlReceiver) || cfg.ControlReceiver == cfg.ControlService {
			return errors.New("controlReceiver must be a distinct local service name")
		}
		// The TCP plugins share a binary. Use the exact object from this
		// scope so an old handle cannot follow the name to a different row.
		var link *control
		if err := row.Use(cfg.ControlService, &link); err != nil {
			return err
		}
		var receiver struct {
			Attach func(context.Context, any) error
		}
		if err := row.Use(cfg.ControlReceiver, &receiver); err != nil {
			return err
		}
		return receiver.Attach(row, rutis.Ref(link))
	},
})
