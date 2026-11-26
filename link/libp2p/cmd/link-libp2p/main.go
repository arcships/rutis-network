package main

import (
	"fmt"
	"os"

	link "github.com/arcships/rutis-network/link/libp2p"
	"github.com/arcships/rutis/go/rutis"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "keygen" {
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: link-libp2p keygen <key-file>")
			os.Exit(2)
		}
		id, err := link.GenerateKey(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(id)
		return
	}
	rutis.Serve(link.Plugin)
}
