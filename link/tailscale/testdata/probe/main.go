package main

import "github.com/arcships/rutis/go/rutis"

type witness struct{}

func (*witness) Echo(value string) string { return value }

type config struct {
	Service string `json:"service"`
}

func main() {
	rutis.Serve(rutis.Define(rutis.Plugin[config]{
		Name: "tailnet-probe",
		Apply: func(row *rutis.Ctx, cfg config) error {
			if cfg.Service == "" {
				cfg.Service = "witness"
			}
			row.Provide(cfg.Service, &witness{})
			return nil
		},
	}))
}
