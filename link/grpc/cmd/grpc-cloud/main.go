// grpc-cloud serves one directory or one regional relay from a JSON file.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	link "github.com/arcships/rutis-network/link/grpc"
	"github.com/arcships/rutis-network/link/grpc/wire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type regionalConfig struct {
	link.Identity
	Address    string `json:"address"`
	ServerName string `json:"serverName"`
	Region     string `json:"region"`
	MaxStreams int    `json:"maxStreams"`
}
type config struct {
	link.Identity
	Listen  string              `json:"listen"`
	Control *link.ControlConfig `json:"control"`
	Relay   *regionalConfig     `json:"relay"`
}

func serve(ctx context.Context, cfg config) error {
	if cfg.Listen == "" || (cfg.Control == nil) == (cfg.Relay == nil) {
		return errors.New("listen and exactly one of control or relay are required")
	}
	tc, err := cfg.TLSConfig()
	if err != nil {
		return err
	}
	tc.ClientAuth = tls.VerifyClientCertIfGiven
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tc)), grpc.MaxRecvMsgSize(link.MaxWireSize), grpc.MaxSendMsgSize(link.MaxWireSize))
	l, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer l.Close()
	run, stop := context.WithCancel(ctx)
	defer stop()
	if c := cfg.Control; c != nil {
		directory, err := link.NewControl(*c)
		if err != nil {
			return err
		}
		defer directory.Close()
		wire.RegisterCloudServer(server, directory)
	} else {
		agent := cfg.Relay
		conn, err := link.DialControl(agent.Address, agent.ServerName, agent.Identity)
		if err != nil {
			return err
		}
		defer conn.Close()
		r, err := link.NewRelay(wire.NewCloudClient(conn), agent.Region, agent.MaxStreams, 20*time.Second)
		if err != nil {
			return err
		}
		wire.RegisterCloudRelayServer(server, r)
		finished := make(chan struct{})
		defer func() { stop(); <-finished }()
		go func() { defer close(finished); r.Run(run) }()
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(l) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	stop()
	fallback := time.AfterFunc(5*time.Second, server.Stop)
	defer fallback.Stop()
	server.GracefulStop()
	return <-done
}
func execute() error {
	if len(os.Args) != 2 {
		return errors.New("usage: grpc-cloud CONFIG.json")
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	var cfg config
	if err := d.Decode(&cfg); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, cfg)
}
func main() {
	if err := execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
