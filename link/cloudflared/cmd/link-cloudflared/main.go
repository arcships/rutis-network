package main

import (
	"github.com/arcships/rutis-network/link/cloudflared"
	"github.com/arcships/rutis/go/rutis"
)

func main() { rutis.Serve(cloudflared.Plugin) }
