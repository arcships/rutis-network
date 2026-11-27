package main

import (
	"github.com/arcships/rutis-network/group/peer"
	"github.com/arcships/rutis/go/rutis"
)

func main() { rutis.Serve(peer.Plugin) }
