// Command turn runs the authenticated loopback STUN/TURN server for the demo and tests.
package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/pion/turn/v5"
)

func main() {
	secret := os.Getenv("TURN_SECRET")
	if secret == "" {
		log.Fatal("TURN_SECRET is required")
	}
	const host = "127.0.0.1"
	ip := net.ParseIP(host)
	addr := net.JoinHostPort(host, "0")
	if len(os.Args) > 1 {
		addr = net.JoinHostPort(host, os.Args[1])
	}
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		log.Fatal(err)
	}
	server, err := turn.NewServer(turn.ServerConfig{
		Realm:       "rutis.test",
		AuthHandler: turn.LongTermTURNRESTAuthHandler(secret, nil),
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn:            conn,
			PermissionHandler:     func(_ net.Addr, peer net.IP) bool { return peer.IsLoopback() },
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: ip, Address: host},
		}},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer server.Close()
	fmt.Println("TURN", conn.LocalAddr())
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)
	<-done
}
