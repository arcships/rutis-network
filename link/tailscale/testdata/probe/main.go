package main

import "github.com/arcships/rutis/go/rutis"

type witness struct{}

func (*witness) Echo(value string) string { return value }

func main() {
	rutis.Serve(rutis.Define(rutis.Plugin[rutis.NoConfig]{
		Name: "tailnet-probe",
		Apply: func(row *rutis.Ctx, _ rutis.NoConfig) error {
			row.Provide("witness", &witness{})
			return nil
		},
	}))
}
