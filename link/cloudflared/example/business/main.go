package main

import (
	"context"
	"github.com/arcships/rutis/go/rutis"
)

type Greeter struct{}

func (*Greeter) Hello(name string) string { return "office: hello " + name }

var GreeterPlugin = rutis.Define(rutis.Plugin[rutis.NoConfig]{
	Name: "greeter",
	Apply: func(row *rutis.Ctx, _ rutis.NoConfig) error {
		row.Provide("greeter", &Greeter{})
		return nil
	},
})

type GreeterRef struct {
	Hello func(context.Context, string) (string, error)
}

type Consumer struct{ greeter GreeterRef }

func (c *Consumer) Greet(ctx context.Context, name string) (string, error) {
	return c.greeter.Hello(ctx, name)
}

var ConsumerPlugin = rutis.Define(rutis.Plugin[rutis.NoConfig]{
	Name:   "consumer",
	Inject: []string{"greeter"},
	Apply: func(row *rutis.Ctx, _ rutis.NoConfig) error {
		var g GreeterRef
		if err := row.Use("greeter", &g); err != nil {
			return err
		}
		row.Provide("consumer", &Consumer{greeter: g})
		return nil
	},
})

func main() { rutis.Serve(GreeterPlugin, ConsumerPlugin) }
