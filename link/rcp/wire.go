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
	credential   failure = "credential-rejected"
	expired      failure = "credential-expired"
	incompatible failure = "version-mismatch"
	badFrame     failure = "invalid-frame"
	oversized    failure = "frame-too-large"
	offline      failure = "peer-offline"
	unavailable  failure = "relay-unavailable"
	transport    failure = "transport-error"
	replaced     failure = "replaced"
	refused      failure = "refused"
	done         failure = "done"
	peerClosed   failure = "peer-closed"
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

func readPair(conn *websocket.Conn, hs *noise.HandshakeState) ([]byte, *noise.CipherState, *noise.CipherState, error) {
	typ, b, err := conn.ReadMessage()
	if err != nil {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return nil, nil, nil, offline
		}
		return nil, nil, nil, readFailure(err)
	}
	if typ != websocket.BinaryMessage || len(b) < 4 {
		return nil, nil, nil, badFrame
	}
	switch string(b) {
	case rejectVersion:
		return nil, nil, nil, incompatible
	case rejectCredential:
		return nil, nil, nil, credential
	}
	if string(b[:4]) != version {
		return nil, nil, nil, incompatible
	}
	if len(b) != 4+32+8+16 {
		return nil, nil, nil, badFrame
	}
	payload, c1, c2, err := hs.ReadMessage(nil, b[4:])
	if err != nil {
		return nil, nil, nil, credential
	}
	return payload, c1, c2, nil
}

func writePair(conn *websocket.Conn, hs *noise.HandshakeState, payload []byte) (*noise.CipherState, *noise.CipherState, error) {
	msg, c1, c2, err := hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, nil, credential
	}
	if err = conn.WriteMessage(websocket.BinaryMessage, append([]byte(version), msg...)); err != nil {
		return nil, nil, transport
	}
	return c1, c2, nil
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
	if client {
		if _, _, err = writePair(conn, hs, mine); err != nil {
			return nil, err
		}
	}
	theirs, c1, c2, err := readPair(conn, hs)
	if err != nil {
		return nil, err
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
		c1, c2, err = writePair(conn, hs, mine)
		if err != nil {
			return nil, err
		}
		ch.send, ch.recv = c2, c1
	}
	// Explicit confirmation proves the peer possesses this connection's split keys.
	// Rejections are binary messages so an opaque relay forwards them too.
	typ, confirmation, err := conn.ReadMessage()
	if err != nil {
		return nil, readFailure(err)
	}
	if typ != websocket.BinaryMessage {
		return nil, badFrame
	}
	switch string(confirmation) {
	case rejectCredential:
		return nil, credential
	case rejectVersion:
		return nil, incompatible
	}
	kind, _, err := unseal(ch.recv, confirmation)
	if err != nil {
		return nil, err
	}
	if kind != ready {
		return nil, badFrame
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

func unseal(state *noise.CipherState, frame []byte) (byte, []byte, error) {
	if len(frame) < overhead || !bytes.Equal(frame[:8], []byte{0, 0, 0, 12, 0, 0, 0, 0}) || binary.BigEndian.Uint64(frame[8:16]) != state.Nonce() {
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
	if len(frame) > ch.limit+overhead {
		return 0, nil, oversized
	}
	return unseal(ch.recv, frame)
}
