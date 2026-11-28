// Small host fixture with a unique service name per peer.
package main

import (
	"github.com/arcships/rutis/go/rutis"
	"strings"
)

type witness struct{ id string }

func (w *witness) Echo(value string) string { return w.id + " " + value }
func (w *witness) Blob(size int) string     { return strings.Repeat("a", size) }

type config struct {
	Service string `json:"service"`
}

func main() {
	rutis.Serve(rutis.Define(rutis.Plugin[config]{Name: "rcp-witness", Apply: func(row *rutis.Ctx, c config) error { row.Provide(c.Service, &witness{id: c.Service}); return nil }}))
}
