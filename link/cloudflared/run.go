package cloudflared

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func loopback(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }

func readSecret(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("invalid-credentials")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("invalid-credentials")
	}
	defer file.Close()
	const limit = 16 << 10
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	value := strings.TrimSpace(string(data))
	if err != nil || len(data) > limit || value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("invalid-credentials")
	}
	return value, nil
}

// Explicit environment: neither an inherited TUNNEL_TOKEN nor debugging flags
// should override the row's settings. Child stdout is reserved for stream data.
func command(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = []string{}
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "SystemRoot", "USERPROFILE", "TEMP", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	return cmd
}

func (c *control) tcpStatus(ctx context.Context) (tcpState, error) {
	state, err := c.tcp.Status(ctx)
	if err != nil {
		return tcpState{}, errors.New("tcp-status-failed")
	}
	c.update(func(s *snapshot) {
		s.TCPPhase, s.TCPError, s.TCPRetryAt = state.Phase, state.LastError, state.RetryAt
	})
	return state, nil
}

func (c *control) startTCP(ctx context.Context, port int, p profile) error {
	state, err := c.tcpStatus(ctx)
	if err != nil {
		return err
	}
	role := "listen"
	patch := map[string]any{"port": port}
	if c.cfg.Role == "dial" {
		role = "dial"
		patch["host"], patch["servername"], patch["reconnect"] = "127.0.0.1", p.ServerName, true
	}
	if state.Role != role || state.Desired != "stopped" {
		return errors.New("tcp-instance-not-idle-or-wrong-role")
	}
	if err := c.tcp.Configure(ctx, patch); err != nil {
		return errors.New("tcp-configure-failed")
	}
	c.ownsTCP = true // Start may have taken effect even if its reply is lost.
	if err := c.tcp.Start(ctx); err != nil {
		return errors.New("tcp-start-failed")
	}
	if role == "listen" {
		for {
			state, err := c.tcpStatus(ctx)
			if err != nil {
				return err
			}
			if state.Phase == "listening" && state.BoundPort == port {
				break
			}
			if state.Phase == "failed" || !delay(ctx, 50*time.Millisecond) {
				return errors.New("tcp-listen-failed")
			}
		}
	}
	return nil
}

func delay(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *control) run(ctx context.Context, name string) error {
	binary := c.cfg.Binary
	if binary == "" {
		binary = "cloudflared"
	}
	binary, err := exec.LookPath(binary)
	if err != nil {
		return errors.New("binary-unavailable")
	}
	var listener net.Listener
	port := c.cfg.OriginPort
	p := c.cfg.Profiles[name]
	if c.cfg.Role == "dial" {
		listener, err = (&net.ListenConfig{}).Listen(ctx, "tcp4", loopback(*c.cfg.ForwardPort))
		if err != nil {
			if errors.Is(err, syscall.EADDRINUSE) {
				return errors.New("address-in-use")
			}
			return errors.New("bind-failed")
		}
		defer listener.Close()
		bound := listener.Addr().(*net.TCPAddr).Port
		port = &bound
		c.update(func(s *snapshot) { s.BoundPort = bound })
	} else if _, err := readSecret(c.cfg.TunnelTokenFile); err != nil {
		return err
	}
	if c.tcp != nil {
		if err := c.startTCP(ctx, *port, p); err != nil {
			return err
		}
		watchCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for delay(watchCtx, time.Second) {
				if _, err := c.tcpStatus(watchCtx); err != nil && watchCtx.Err() == nil {
					c.update(func(s *snapshot) { s.TCPPhase, s.TCPError = "unknown", "tcp-status-failed" })
				}
			}
		}()
		defer func() { cancel(); <-done }()
	}
	if listener != nil {
		return c.forward(ctx, listener, binary, p)
	}
	return c.connector(ctx, binary)
}

func (c *control) forward(ctx context.Context, listener net.Listener, binary string, p profile) error {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	closeOnStop := context.AfterFunc(ctx, func() { listener.Close() })
	defer func() { cancel(); listener.Close(); workers.Wait(); closeOnStop() }()
	c.update(func(s *snapshot) { s.Phase = "listening" })
	// A dial row owns one stream. A stalled local caller cannot fan out
	// cloudflared processes or queue up additional sessions.
	slot := make(chan struct{}, 1)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return errors.New("accept-ended")
		}
		select {
		case slot <- struct{}{}:
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-slot }()
				err := c.stream(ctx, conn, binary, p)
				c.update(func(s *snapshot) {
					s.Process = "stopped"
					if err != nil && ctx.Err() == nil {
						s.LastError = err.Error()
					}
				})
			}()
		default:
			conn.Close()
		}
	}
}

func (c *control) stream(ctx context.Context, conn net.Conn, binary string, p profile) error {
	defer conn.Close()
	id, err := readSecret(p.ClientIDFile)
	if err != nil {
		return err
	}
	secret, err := readSecret(p.ClientSecretFile)
	if err != nil {
		return err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := command(streamCtx, binary, "access", "tcp", "--hostname", p.Hostname)
	cmd.Env = append(cmd.Env, "TUNNEL_SERVICE_TOKEN_ID="+id, "TUNNEL_SERVICE_TOKEN_SECRET="+secret)
	input, err := cmd.StdinPipe()
	if err != nil {
		return errors.New("pipe-failed")
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return errors.New("pipe-failed")
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		return errors.New("child-start-failed")
	}
	c.update(func(s *snapshot) { s.Process = "running" })
	// The first EOF closes the whole link, including the other copy operation.
	finish := sync.OnceFunc(func() {
		cancel()
		conn.Close()
		input.Close()
		output.Close()
	})
	stop := context.AfterFunc(ctx, finish)
	inputDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(input, conn)
		finish()
		close(inputDone)
	}()
	_, _ = io.Copy(conn, output)
	finish()
	<-inputDone
	stop()
	_ = cmd.Wait()
	return errors.New("stream-ended")
}

var edgeHTTP = &http.Client{
	Transport: &http.Transport{DisableKeepAlives: true}, Timeout: time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

var yes, no = true, false

func (c *control) pollEdge(ctx context.Context, port int) {
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+loopback(port)+"/ready", nil)
		resp, err := edgeHTTP.Do(req)
		var ready *bool
		if err == nil {
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK:
				ready = &yes
			case http.StatusServiceUnavailable:
				ready = &no
			}
		}
		c.update(func(s *snapshot) { s.EdgeReady = ready })
		if !delay(ctx, time.Second) {
			break
		}
	}
}

func (c *control) connector(ctx context.Context, binary string) error {
	metricsPort := 0
	if c.cfg.MetricsPort != nil {
		metricsPort = *c.cfg.MetricsPort
	}
	args := []string{"tunnel", "--no-autoupdate", "--metrics", loopback(metricsPort), "run", "--token-file", c.cfg.TunnelTokenFile}
	backoff := 500 * time.Millisecond
	for ctx.Err() == nil {
		cmd := command(ctx, binary, args...)
		if err := cmd.Start(); err != nil {
			return errors.New("child-start-failed")
		}
		started := time.Now()
		c.update(func(s *snapshot) { s.Phase, s.Process, s.RetryAt = "running", "running", "" })
		probeCtx, cancel := context.WithCancel(ctx)
		var probe sync.WaitGroup
		if c.cfg.MetricsPort != nil {
			probe.Add(1)
			go func() { defer probe.Done(); c.pollEdge(probeCtx, *c.cfg.MetricsPort) }()
		}
		_ = cmd.Wait()
		cancel()
		probe.Wait()
		if ctx.Err() != nil {
			break
		}
		if time.Since(started) >= time.Minute {
			backoff = 500 * time.Millisecond
		}
		c.update(func(s *snapshot) {
			s.Phase, s.Process, s.LastError, s.EdgeReady = "retrying", "stopped", "connector-exited", nil
			s.RetryAt = time.Now().Add(backoff).UTC().Format(time.RFC3339Nano)
		})
		if !delay(ctx, backoff) {
			break
		}
		backoff = min(2*backoff, 30*time.Second)
	}
	return ctx.Err()
}
