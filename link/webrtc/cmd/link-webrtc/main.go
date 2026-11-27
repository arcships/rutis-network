package main

import (
	link "github.com/arcships/rutis-network/link/webrtc"
	"github.com/arcships/rutis/go/rutis"
)

func main() { rutis.Serve(link.Plugin, link.HTTPProvider) }
