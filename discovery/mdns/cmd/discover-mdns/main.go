package main

import (
	"github.com/arcships/rutis-network/discovery/mdns"
	"github.com/arcships/rutis-network/discovery/mdns/tcpoffer"
	"github.com/arcships/rutis/go/rutis"
)

func main() { rutis.Serve(mdns.Plugin, tcpoffer.Source, tcpoffer.Dial) }
