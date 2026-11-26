package main

import (
	"github.com/arcships/rutis-network/link/tcp"
	"github.com/arcships/rutis/go/rutis"
)

func main() { rutis.Serve(tcp.Dial, tcp.Listen) }
