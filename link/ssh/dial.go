package linkssh

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
	"github.com/skeema/knownhosts"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

var (
	errCredentials = errors.New("invalid-credentials")
	errHostKey     = errors.New("ssh-host-key-rejected")
	errHandshake   = errors.New("ssh-handshake-failed")
	errForward     = errors.New("ssh-forward-denied")
	errTarget      = errors.New("ssh-target-unavailable")
	errAlgorithms  = errors.New("incompatible-ssh")
)

// Each session owns its outer SSH socket. Closing it interrupts channel reads,
// writes and window waits even when the server no longer services the channel.
type tunnel struct {
	net.Conn
	outer net.Conn
}

func (t *tunnel) Close() error { return t.outer.Close() }

func authenticate(ctx context.Context, raw net.Conn, address string, p profile) (*ssh.Client, error) {
	hosts, err := knownhosts.NewDB(p.KnownHosts)
	if err != nil {
		return nil, errCredentials
	}
	var auth ssh.AuthMethod
	if p.IdentityFile != "" {
		key, err := os.ReadFile(p.IdentityFile)
		if err != nil {
			return nil, errCredentials
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, errCredentials
		}
		auth = ssh.PublicKeys(signer)
	} else {
		socket, err := (&net.Dialer{}).DialContext(ctx, "unix", os.Getenv("SSH_AUTH_SOCK"))
		if err != nil {
			return nil, errCredentials
		}
		defer socket.Close()
		stop := context.AfterFunc(ctx, func() { socket.Close() })
		defer stop()
		auth = ssh.PublicKeysCallback(agent.NewClient(socket).Signers)
	}
	// Prefer a pinned key on multi-key servers. Keep other supported host
	// algorithms available for certificates signed by a trusted host CA.
	algorithms := ssh.SupportedAlgorithms()
	pinned := hosts.HostKeyAlgorithms(address)
	hostKeys := algorithms.HostKeys
	sort.SliceStable(hostKeys, func(i, j int) bool {
		return slices.Contains(pinned, hostKeys[i]) && !slices.Contains(pinned, hostKeys[j])
	})
	trust := hosts.HostKeyCallback()
	cfg := &ssh.ClientConfig{
		User:              p.User,
		Auth:              []ssh.AuthMethod{auth},
		HostKeyAlgorithms: hostKeys,
		Config:            ssh.Config{KeyExchanges: algorithms.KeyExchanges, Ciphers: algorithms.Ciphers, MACs: algorithms.MACs},
		HostKeyCallback: func(host string, addr net.Addr, key ssh.PublicKey) error {
			if err := trust(host, addr, key); err != nil {
				return errHostKey
			}
			return nil
		},
	}
	conn, channels, requests, err := ssh.NewClientConn(raw, address, cfg)
	if err != nil {
		var alg *ssh.AlgorithmNegotiationError
		switch {
		case errors.Is(err, errHostKey):
			return nil, errHostKey
		case errors.As(err, &alg):
			return nil, errAlgorithms
		case tcp.TransportErrorCode(err) != "connection-failed":
			return nil, err
		default:
			// x/crypto has no structured client-side user-auth rejection.
			return nil, errHandshake
		}
	}
	return ssh.NewClient(conn, channels, requests), nil
}

func dial(ctx context.Context, e endpoint, p profile, handshaking func(net.Conn)) (conn net.Conn, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	identity, err := tcp.LoadTLSCredentials(p.CA, p.Cert, p.Key)
	if err != nil {
		return nil, err
	}
	identity.ServerName = e.ServerName
	if identity.ServerName == "" {
		host, _, _ := net.SplitHostPort(e.TargetAddress)
		identity.ServerName, _, _ = strings.Cut(host, "%")
	}
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", e.SSHAddress)
	if err != nil {
		return nil, err
	}
	// NewClientConn does not take a context. Closing the outer socket also
	// interrupts a stalled SSH exchange, channel request or inner TLS handshake.
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer func() {
		if !stop() {
			conn, err = nil, ctx.Err()
		}
		if err != nil {
			raw.Close()
		}
	}()
	handshaking(raw)
	client, err := authenticate(ctx, raw, e.SSHAddress, p)
	if err != nil {
		return nil, err
	}
	stream, err := client.DialContext(ctx, "tcp", e.TargetAddress)
	if err != nil {
		var reject *ssh.OpenChannelError
		if errors.As(err, &reject) {
			switch reject.Reason {
			case ssh.Prohibited, ssh.UnknownChannelType:
				return nil, errForward
			case ssh.ConnectionFailed:
				return nil, errTarget
			}
		}
		return nil, err
	}
	secure := tls.Client(stream, identity)
	if err := secure.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return &tunnel{Conn: secure, outer: raw}, nil
}

func errorCode(err error) string {
	for _, code := range []error{errCredentials, errHostKey, errHandshake, errForward, errTarget, errAlgorithms} {
		if errors.Is(err, code) {
			return code.Error()
		}
	}
	return tcp.TransportErrorCode(err)
}

func retryDelay(code string, backoff time.Duration) time.Duration {
	switch code {
	case "ssh-host-key-rejected", "ssh-forward-denied", "incompatible-ssh", "incompatible-tls":
		return 0
	case "invalid-credentials", "ssh-handshake-failed", "certificate-rejected":
		return 30 * time.Second
	default:
		return backoff
	}
}
