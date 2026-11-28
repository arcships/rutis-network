// Package rcp connects a rutis session to a pinned device over a direct or relayed WebSocket.
package rcp

import (
	"bytes"
	"crypto/ecdh"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"

	"github.com/flynn/noise"
	"github.com/gorilla/websocket"
)

const (
	version          = "RCP\x01"
	rejectVersion    = "RCPE\x01"
	rejectCredential = "RCPE\x02"
	defaultLimit     = 16 << 20
	// Four bytes of nonce length, twelve of nonce, one of type, sixteen of tag.
	overhead         = 33
	handshakeTimeout = 10 * time.Second
	offerInterval    = time.Second
	pairSize         = len(version) + 32 + 8 + 16
)
const (
	ready byte = iota
	dataMessage
	ping
	pong
	closeDone
	closeReplaced
	closeRejected
)

type failure string

func (f failure) Error() string { return string(f) }

const (
	credential    failure = "credential-rejected"
	expired       failure = "credential-expired"
	incompatible  failure = "version-mismatch"
	badFrame      failure = "invalid-frame"
	oversized     failure = "frame-too-large"
	offline       failure = "peer-offline"
	unavailable   failure = "relay-unavailable"
	transport     failure = "transport-error"
	replaced      failure = "replaced"
	refused       failure = "refused"
	done          failure = "done"
	peerClosed    failure = "peer-closed"
	peerRestarted failure = "peer-restarted"
)

type channel struct {
	conn       *websocket.Conn
	send, recv *noise.CipherState
	limit      int
	beat       time.Duration
}

func pairState(c Credentials, random io.Reader) (*noise.HandshakeState, error) {
	private, err := ecdh.X25519().NewPrivateKey(c.PrivateKey)
	if err != nil || len(c.PeerKey) != 32 {
		return nil, credential
	}
	// Both peers pin the remote static public key through the credential provider.
	return noise.NewHandshakeState(noise.Config{
		CipherSuite: noise.NewCipherSuite(noise.DH25519, noise.CipherAESGCM, noise.HashSHA256),
		Pattern:     noise.HandshakeKK, Initiator: c.Role == "client", Random: random,
		StaticKeypair: noise.DHKey{Private: c.PrivateKey, Public: private.PublicKey().Bytes()},
		PeerStatic:    c.PeerKey, Prologue: append([]byte(version), c.Binding...),
	})
}

func offer(limit int, beat time.Duration) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b, uint32(limit))
	binary.BigEndian.PutUint32(b[4:], uint32(beat.Milliseconds()))
	return b
}

func readHandshakeFrame(conn *websocket.Conn) ([]byte, error) {
	typ, b, err := conn.ReadMessage()
	if err != nil {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return nil, offline
		}
		return nil, readFailure(err)
	}
	if typ != websocket.BinaryMessage || len(b) < 4 {
		return nil, badFrame
	}
	switch string(b) {
	case rejectVersion:
		return nil, incompatible
	case rejectCredential:
		return nil, credential
	}
	if bytes.HasPrefix(b, []byte("RCP")) && string(b[:4]) != version {
		return nil, incompatible
	}
	return b, nil
}

func isPair(frame []byte) bool {
	return len(frame) == pairSize && bytes.HasPrefix(frame, []byte(version))
}

// A forwarding relay may drop the first offer while the daemon is offline.
// Resend the identical bytes on the same socket until a response arrives.
// Join the writer before READY or session traffic can use the socket.
func receivePair(conn *websocket.Conn, first []byte) ([]byte, error) {
	if first != nil {
		if err := conn.WriteMessage(websocket.BinaryMessage, first); err != nil {
			return nil, transport
		}
		stop, finished := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(finished)
			ticker := time.NewTicker(offerInterval)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					if conn.WriteMessage(websocket.BinaryMessage, first) != nil {
						conn.Close()
						return
					}
				}
			}
		}()
		defer func() { close(stop); <-finished }()
	}
	for {
		b, err := readHandshakeFrame(conn)
		if err != nil {
			return nil, err
		}
		if isPair(b) {
			return b, nil
		}
		// A newly dialed socket may still receive records from the old peer.
		// They are never decrypted or delivered in this handshake.
		if !hasRecordHeader(b) {
			return nil, badFrame
		}
	}
}

func handshake(conn *websocket.Conn, c Credentials, limit int, beat time.Duration) (*channel, error) {
	hs, err := pairState(c, nil)
	if err != nil {
		return nil, err
	}
	until := time.Now().Add(handshakeTimeout)
	if !c.ExpiresAt.IsZero() && c.ExpiresAt.Before(until) {
		until = c.ExpiresAt
	}
	conn.SetReadLimit(128)
	conn.SetReadDeadline(until)
	conn.SetWriteDeadline(until)
	mine := offer(limit, beat)
	client := c.Role == "client"
	var first []byte
	if client {
		first, _, _, err = hs.WriteMessage(nil, mine)
		if err != nil {
			return nil, credential
		}
		first = append([]byte(version), first...)
	}
	peerOffer, err := receivePair(conn, first)
	if err != nil {
		return nil, err
	}
	theirs, c1, c2, err := hs.ReadMessage(nil, peerOffer[len(version):])
	if err != nil {
		return nil, credential
	}
	peerLimit := int64(binary.BigEndian.Uint32(theirs))
	peerBeat := time.Duration(binary.BigEndian.Uint32(theirs[4:])) * time.Millisecond
	if peerLimit < 1 || peerLimit > maxLimit || peerBeat < minBeat || peerBeat > maxBeat {
		return nil, badFrame
	}
	ch := &channel{conn: conn, limit: int(min(int64(limit), peerLimit)), beat: max(beat, peerBeat)}
	if client {
		ch.send, ch.recv = c1, c2
		if err = ch.write(ready, nil); err != nil {
			return nil, err
		}
	} else {
		var response []byte
		response, c1, c2, err = hs.WriteMessage(nil, mine)
		if err != nil {
			return nil, credential
		}
		if conn.WriteMessage(websocket.BinaryMessage, append([]byte(version), response...)) != nil {
			return nil, transport
		}
		ch.send, ch.recv = c2, c1
	}
	// Explicit confirmation proves the peer possesses this connection's split keys.
	// Rejections are binary messages so an opaque relay forwards them too.
	for {
		confirmation, err := readHandshakeFrame(conn)
		if err != nil {
			return nil, err
		}
		// Retransmissions can already be in flight when the response is sent.
		if !client && bytes.Equal(confirmation, peerOffer) {
			continue
		}
		if isPair(confirmation) {
			return nil, peerRestarted
		}
		kind, _, err := unseal(ch.recv, confirmation)
		if err != nil {
			return nil, err
		}
		if kind != ready {
			return nil, badFrame
		}
		break
	}
	if !client {
		if err = ch.write(ready, nil); err != nil {
			return nil, err
		}
	}
	if !c.ExpiresAt.IsZero() && !time.Now().Before(c.ExpiresAt) {
		return nil, expired
	}
	conn.SetReadLimit(int64(ch.limit) + overhead)
	return ch, nil
}

func seal(state *noise.CipherState, kind byte, data []byte) ([]byte, error) {
	var header [16]byte
	binary.BigEndian.PutUint32(header[:4], 12)
	binary.BigEndian.PutUint64(header[8:], state.Nonce())
	plaintext := make([]byte, 1+len(data))
	plaintext[0] = kind
	copy(plaintext[1:], data)
	return state.Encrypt(header[:], header[:], plaintext)
}

func hasRecordHeader(frame []byte) bool {
	return len(frame) >= overhead && binary.BigEndian.Uint32(frame[:4]) == 12 && binary.BigEndian.Uint32(frame[4:8]) == 0
}

func unseal(state *noise.CipherState, frame []byte) (byte, []byte, error) {
	if !hasRecordHeader(frame) || binary.BigEndian.Uint64(frame[8:16]) != state.Nonce() {
		return 0, nil, badFrame
	}
	plain, err := state.Decrypt(nil, frame[:16], frame[16:])
	if err != nil || plain[0] > closeRejected || (plain[0] != dataMessage && len(plain) != 1) {
		return 0, nil, badFrame
	}
	return plain[0], plain[1:], nil
}

func (ch *channel) write(kind byte, data []byte) error {
	if len(data) > ch.limit {
		return oversized
	}
	frame, err := seal(ch.send, kind, data)
	if err != nil {
		return badFrame
	}
	if err = ch.conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		return transport
	}
	return nil
}

func readFailure(err error) error {
	if errors.Is(err, websocket.ErrReadLimit) {
		return oversized
	}

	return transport
}
func (ch *channel) read() (byte, []byte, error) {
	typ, frame, err := ch.conn.ReadMessage()
	if err != nil {
		return 0, nil, readFailure(err)
	}
	if typ != websocket.BinaryMessage {
		return 0, nil, badFrame
	}
	if isPair(frame) {
		return 0, nil, peerRestarted
	}
	if len(frame) > ch.limit+overhead {
		return 0, nil, oversized
	}
	return unseal(ch.recv, frame)
}
