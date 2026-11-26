// The mounting plugin and link-tcp run in separate Go runtimes.
package main

import (
	"context"
	"sync"

	"github.com/arcships/rutis/go/rutis"
	"github.com/arcships/rutis/go/rutis/advanced"
)

type owner struct {
	mu       sync.Mutex
	control  *advanced.Service
	attached chan struct{}
	loader   struct {
		Create func(context.Context, map[string]any, string) (string, error)
		Remove func(context.Context, string) error
	}
}

// Attach is the private receiver called by link-tcp-attach when both rows are available.
func (o *owner) Attach(handle *advanced.Service) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.control = handle
	close(o.attached)
	o.attached = make(chan struct{})
}

// The test driver uses the same instance to mount, control and remove the row.
type driver struct{ owner *owner }

func (d *driver) Mount(ctx context.Context, config map[string]any) (string, error) {
	o := d.owner
	o.mu.Lock()
	attached := o.attached
	o.mu.Unlock()
	id, err := o.loader.Create(ctx, map[string]any{
		"id": "wire", "name": "go:link-tcp-listen",
		"config": config,
	}, "local-links")
	if err != nil {
		return "", err
	}
	// The row ID may be available while its runtime is still starting.
	select {
	case <-attached:
		return id, nil
	case <-ctx.Done():
		return id, ctx.Err()
	}
}

func (d *driver) Remove(ctx context.Context) error {
	return d.owner.loader.Remove(ctx, "wire")
}

func (d *driver) Invoke(ctx context.Context, method string, args []any) (any, error) {
	d.owner.mu.Lock()
	for d.owner.control == nil {
		attached := d.owner.attached
		d.owner.mu.Unlock()
		select {
		case <-attached:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		d.owner.mu.Lock()
	}
	h := d.owner.control
	d.owner.mu.Unlock()
	var result any
	err := h.Call(ctx, method, &result, args...)
	return result, err
}

func main() {
	rutis.Serve(rutis.Define(rutis.Plugin[rutis.NoConfig]{
		Name: "mounter", Inject: []string{"rutis/loader"},
		Provides: rutis.Provides{
			"office-owner": rutis.MethodsOf[*owner](),
			"test-driver":  rutis.MethodsOf[*driver](),
		},
		Apply: func(row *rutis.Ctx, _ rutis.NoConfig) error {
			o := &owner{attached: make(chan struct{})}
			if err := row.Use("rutis/loader", &o.loader); err != nil {
				return err
			}
			row.Provide("office-owner", o)
			row.Provide("test-driver", &driver{o})
			return nil
		},
	}))
}
