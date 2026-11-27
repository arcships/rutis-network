package main

import (
	"context"
	"errors"
	"github.com/arcships/rutis/go/rutis"
)

type jobService struct {
	SubmitJob func(context.Context, string, func(context.Context, string) error) (string, error)
}
type device struct{ jobs jobService }
type receipt struct {
	ID       string `json:"id"`
	Progress string `json:"progress"`
}

func (d *device) Submit(ctx context.Context, name string) (receipt, error) {
	progress := make(chan string, 1)
	id, err := d.jobs.SubmitJob(ctx, name, func(_ context.Context, event string) error {
		select {
		case progress <- event:
			return nil
		default:
			return errors.New("duplicate progress event")
		}
	})
	if err != nil {
		return receipt{}, err
	}
	select {
	case event := <-progress:
		return receipt{ID: id, Progress: event}, nil
	default:
		return receipt{}, errors.New("cloud returned before reporting progress")
	}
}

var plugin = rutis.Define(rutis.Plugin[rutis.NoConfig]{
	Name: "device", Inject: []string{"jobs"},
	Apply: func(row *rutis.Ctx, _ rutis.NoConfig) error {
		var j jobService
		if err := row.Use("jobs", &j); err != nil {
			return err
		}
		row.Provide("device", &device{jobs: j})
		return nil
	},
})

func main() { rutis.Serve(plugin) }
