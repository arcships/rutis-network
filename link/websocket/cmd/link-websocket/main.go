package main

import (
	wslink "github.com/arcships/rutis-network/link/websocket"
	"github.com/arcships/rutis/go/rutis"
)

func main() { rutis.Serve(wslink.Listen, wslink.Dial) }
