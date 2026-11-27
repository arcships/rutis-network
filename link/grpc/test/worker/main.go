// Fixtures for verifying remote plugin placement and ambiguous in-flight RPCs.
package main

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/arcships/rutis/go/rutis"
)

type config struct {
	ReceiptFile string `json:"receiptFile"`
}
type jobs struct {
	next     atomic.Uint64
	receipts string
}

func (j *jobs) SubmitJob(ctx context.Context, _ string, cb func(context.Context, string) error) (string, error) {
	id := fmt.Sprintf("job-%d", j.next.Add(1))
	return id, cb(ctx, id+" queued")
}
func (j *jobs) Hold(ctx context.Context) error {
	f, err := os.OpenFile(j.receipts, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteString("accepted\n")
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return nil
	}
}

var worker = rutis.Define(rutis.Plugin[config]{Name: "worker", Apply: func(row *rutis.Ctx, c config) error {
	row.Provide("jobs", &jobs{receipts: c.ReceiptFile})
	return nil
}})

type marker struct{}

func (*marker) Pid() int { return os.Getpid() }

var remote = rutis.Define(rutis.Plugin[rutis.NoConfig]{Name: "remote-marker", Apply: func(row *rutis.Ctx, _ rutis.NoConfig) error { row.Provide("marker", &marker{}); return nil }})

func main() { rutis.Serve(worker, remote) }
