package linkssh

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"golang.org/x/crypto/ssh"
)

// Keep servicing the SSH listener even when all slots are in use. Blocking
// Accept behind a slow peer would stall forwarded channels on the shared client.
const maxReverseStreams = 64

func (c *control) serveReverse(parent context.Context, client *ssh.Client, ln net.Listener, identity *tls.Config, target string) <-chan error {
	if identity != nil {
		identity.ClientAuth = tls.RequireAndVerifyClientCert
	}
	bound := ln.Addr().String()
	c.update(func(s *snapshot) {
		s.Phase, s.BoundAddress, s.LastError, s.LastClientError = "listening", bound, "", ""
	})
	fmt.Fprintln(os.Stderr, "link/ssh listening", bound)
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithCancel(parent)
		released := make(chan struct{})
		context.AfterFunc(ctx, func() {
			// Wait for sshd to acknowledge cancel-tcpip-forward on a live relay.
			// A lost relay cannot hold stop or reconfiguration indefinitely.
			timeout := time.AfterFunc(2*time.Second, func() { client.Close() })
			ln.Close()
			timeout.Stop()
			client.Close()
			close(released)
		})
		var clients sync.WaitGroup
		slots := make(chan struct{}, maxReverseStreams)
		for {
			conn, err := ln.Accept()
			if err != nil {
				cancel()
				select {
				case <-released:
					clients.Wait()
					done <- err
					close(done)
					return
				case <-time.After(20 * time.Millisecond):
					// Accept can fail on a single queued channel confirmation.
					// Drain any remaining offers while x/crypto removes the forward.
					continue
				}
			}
			select {
			case slots <- struct{}{}:
				clients.Add(1)
				go func() {
					defer clients.Done()
					defer func() { <-slots }()
					defer conn.Close()
					err := c.serveStream(ctx, conn, identity, target)
					if err != nil && ctx.Err() == nil {
						c.update(func(s *snapshot) { s.LastClientError = errorCode(err) })
					}
				}()
			default:
				conn.Close()
				c.update(func(s *snapshot) { s.LastClientError = "connection-limit" })
			}
		}
	}()
	return done
}

func (c *control) serveStream(ctx context.Context, stream net.Conn, identity *tls.Config, target string) error {
	if c.role == listenRole {
		secure, err := tcp.AdmitTLS(ctx, stream, identity)
		if err != nil {
			return err
		}
		done, err := tcp.OpenManaged(ctx, c.row, &sessionStream{Conn: secure, transport: stream}, c.sessions)
		if err != nil {
			return err
		}
		defer c.trackStream()()
		return <-done
	}
	local, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", target)
	if err != nil {
		return err
	}
	defer local.Close()
	stop := context.AfterFunc(ctx, func() { local.Close() })
	defer stop()
	defer c.trackStream()()
	return forward(stream, local)
}

func (c *control) trackStream() func() {
	c.update(func(s *snapshot) { s.Connections++ })
	return func() { c.update(func(s *snapshot) { s.Connections-- }) }
}

// A TCP client can finish its request with FIN and still expect a response.
// Forward that half-close in each direction. A copy error terminates both pipes.
func forward(remote, local net.Conn) error {
	results := make(chan error, 2)
	copyTo := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if err == nil {
			if writer, ok := dst.(interface{ CloseWrite() error }); ok {
				err = writer.CloseWrite()
			} else {
				err = errors.New("forwarded connection does not support CloseWrite")
			}
		}
		results <- err
	}
	go copyTo(remote, local)
	go copyTo(local, remote)
	first := <-results
	if first != nil {
		local.Close()
		remote.Close()
	}
	return errors.Join(first, <-results)
}
