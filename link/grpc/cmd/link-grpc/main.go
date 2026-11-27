package main

import (
	link "github.com/arcships/rutis-network/link/grpc"
	"github.com/arcships/rutis/go/rutis"
)

func main() { rutis.Serve(link.ClientPlugin) }
