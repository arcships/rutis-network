package main

import (
	"github.com/arcships/rutis-network/discovery/mdns"
	"github.com/arcships/rutis/go/rutis"
)

func main() { rutis.Serve(mdns.Plugin) }
