// This example cloud service shares its gRPC listener and job scheduler with
// its embedded rutis plugin. Both definitions live in the same Go runtime.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"

	link "github.com/arcships/rutis-network/link/grpc"
	"github.com/arcships/rutis/go/rutis"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type cloudConfig struct {
	link.Identity
	Listen     string `json:"listen"`
	ClientName string `json:"clientName"`
}
type scheduler struct{ next atomic.Uint64 }

func (s *scheduler) submit(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("job name is required")
	}
	return fmt.Sprintf("job-%d", s.next.Add(1)), nil
}

type cloudService struct {
	scheduler scheduler
	endpoint  *link.Endpoint
}

var cloud = rutis.Define(rutis.Plugin[cloudConfig]{
	Name: "cloud-service",
	Apply: func(row *rutis.Ctx, cfg cloudConfig) error {
		tc, err := cfg.TLSConfig()
		if err != nil {
			return err
		}
		server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tc)), grpc.MaxRecvMsgSize(link.MaxWireSize), grpc.MaxSendMsgSize(link.MaxWireSize))
		healthpb.RegisterHealthServer(server, health.NewServer())
		endpoint, err := link.Register(server, cfg.ClientName)
		if err != nil {
			return err
		}
		if cfg.Listen == "" {
			return errors.New("listen address is required")
		}
		l, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			return err
		}
		done := make(chan struct{})
		go func() { defer close(done); _ = server.Serve(l) }()
		row.Effect(func(context.Context) error { server.Stop(); <-done; return nil })
		row.Provide("cloud-runtime", &cloudService{endpoint: endpoint})
		return nil
	},
})

type jobs struct{ queue *scheduler }

func (j *jobs) SubmitJob(ctx context.Context, name string, onProgress func(context.Context, string) error) (string, error) {
	id, err := j.queue.submit(name)
	if err != nil {
		return "", err
	}
	if err := onProgress(ctx, id+" queued"); err != nil {
		return "", err
	}
	return id, nil
}

var embedded = rutis.Define(rutis.Plugin[rutis.NoConfig]{
	Name: "cloud-jobs", Inject: []string{"cloud-runtime", "rutis/sessions"},
	Apply: func(row *rutis.Ctx, _ rutis.NoConfig) error {
		var service *cloudService
		if err := row.Use("cloud-runtime", &service); err != nil {
			return err
		}
		if err := service.endpoint.Bind(row); err != nil {
			return err
		}
		row.Provide("jobs", &jobs{queue: &service.scheduler})
		return nil
	},
})

func main() { rutis.Serve(cloud, embedded) }
