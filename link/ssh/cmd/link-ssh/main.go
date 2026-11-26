package main

import (
	linkssh "github.com/arcships/rutis-network/link/ssh"
	"github.com/arcships/rutis/go/rutis"
)

func main() { rutis.Serve(linkssh.Dial) }
