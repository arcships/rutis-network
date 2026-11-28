package main

import (
	"github.com/arcships/rutis-network/link/rcp"
	"github.com/arcships/rutis/go/rutis"
)

func main() { rutis.Serve(rcp.Plugin(nil)) }
