package tcp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"testing/iotest"
	"time"
)

func TestFraming(t *testing.T) {
	input := []byte{0, 0, 0, 0, 0, 0, 0, 3, 0xff, 0x00, 0xab}
	for _, r := range []io.Reader{bytes.NewReader(input), iotest.OneByteReader(bytes.NewReader(input))} {
		for _, want := range [][]byte{{}, {0xff, 0, 0xab}} {
			got, err := readFrame(r)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("frame %x, err %v", got, err)
			}
		}
		if _, err := readFrame(r); err != io.EOF {
			t.Fatalf("boundary EOF: %v", err)
		}
	}
	for _, data := range [][]byte{{0}, {0, 0, 0, 1}, {0, 0, 0, 2, 7}} {
		if _, err := readFrame(bytes.NewReader(data)); err != errTruncated {
			t.Fatalf("input %x: %v", data, err)
		}
	}
	if _, err := readFrame(bytes.NewReader(binary.BigEndian.AppendUint32(nil, maxFrame+1))); err != errTooLarge {
		t.Fatal(err)
	}
}

func TestEchoInOrder(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	endCalls := 0
	done, err := Open(t.Context(), local, Sessions{Open: func(_ context.Context, far Far) (Near, error) {
		return Near{
			Receive: func(_ context.Context, data []byte) error { return far.Send(data) },
			End: func(_ context.Context, why CloseReason) error {
				endCalls++
				if why.Kind != "done" {
					t.Errorf("end: %v", why)
				}
				return nil
			},
		}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	remote.SetDeadline(time.Now().Add(20 * time.Second))
	for i := range 1000 {
		msg := binary.BigEndian.AppendUint32(nil, uint32(i))
		if err := writeFrame(remote, msg); err != nil {
			t.Fatal(err)
		}
		reply, err := readFrame(remote)
		if err != nil || !bytes.Equal(reply, msg) {
			t.Fatalf("echo %d: %x, %v", i, reply, err)
		}
	}
	remote.Close()
	if err := <-done; err != nil || endCalls != 1 {
		t.Fatalf("end %v, %d calls", err, endCalls)
	}
}

func TestSlowWriterAndMaximumFrame(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	remote.SetDeadline(time.Now().Add(15 * time.Second))
	payload := bytes.Repeat([]byte{0xab}, maxFrame)
	wrote := make(chan error, 1)
	go func() { wrote <- writeFrame(local, payload) }()
	var header [4]byte
	if _, err := io.ReadFull(remote, header[:]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wrote:
		t.Fatal("send completed before body was read")
	default:
	}
	body, err := readFrame(io.MultiReader(bytes.NewReader(header[:]), remote))
	if err != nil || !bytes.Equal(body, payload) {
		t.Fatalf("received %d bytes: %v", len(body), err)
	}
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
}

func TestBridgePreservesFailureDuringCleanup(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
		fail func(net.Conn, func([]byte) error) error
	}{
		{"bad inbound length", errTooLarge, func(c net.Conn, _ func([]byte) error) error {
			_, err := c.Write(binary.BigEndian.AppendUint32(nil, maxFrame+1))
			return err
		}},
		{"truncated", errTruncated, func(c net.Conn, _ func([]byte) error) error {
			if _, err := c.Write([]byte{0}); err != nil {
				return err
			}
			return c.Close()
		}},
		{"oversized send", errTooLarge, func(_ net.Conn, send func([]byte) error) error {
			if err := send(make([]byte, maxFrame+1)); err != errTooLarge {
				return errors.New("oversized send succeeded")
			}
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local, peer := net.Pipe()
			defer peer.Close()
			var send func([]byte) error
			done, err := Open(t.Context(), local, Sessions{Open: func(_ context.Context, far Far) (Near, error) {
				send = far.Send
				return Near{End: func(context.Context, CloseReason) error { return errors.New("cleanup failed") }}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.fail(peer, send); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != tc.want {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestUnloadInterruptsPendingReceive(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	receiving := make(chan struct{})
	done, err := Open(ctx, local, Sessions{Open: func(context.Context, Far) (Near, error) {
		return Near{Receive: func(ctx context.Context, _ []byte) error { close(receiving); <-ctx.Done(); return ctx.Err() }}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = writeFrame(peer, nil); err != nil {
		t.Fatal(err)
	}
	<-receiving
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := readFrame(peer); err != io.EOF {
		t.Fatal(err)
	}
}

func TestSessionCloseInterruptsBlockedSend(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	var far Far
	done, err := Open(t.Context(), local, Sessions{Open: func(_ context.Context, f Far) (Near, error) {
		far = f
		return Near{End: func(context.Context, CloseReason) error {
			t.Error("end echoed a session-initiated close")
			return nil
		}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	wrote := make(chan error, 1)
	go func() { wrote <- far.Send([]byte("payload")) }()
	var header [4]byte
	if _, err := io.ReadFull(peer, header[:]); err != nil {
		t.Fatal(err)
	}
	far.Close(CloseReason{Kind: "done"})
	if err := <-wrote; err == nil {
		t.Fatal("pending write succeeded after close")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPeerReset(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	peer, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	local, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	done, err := Open(t.Context(), local, Sessions{Open: func(context.Context, Far) (Near, error) {
		return Near{End: func(context.Context, CloseReason) error { return nil }}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.(*net.TCPConn).SetLinger(0); err != nil {
		t.Fatal(err)
	}
	peer.Close()
	if err := <-done; errorCode(err) != "ECONNRESET" {
		t.Fatalf("reset: %v", err)
	}
}

func TestStopDuringOpenEndsLateSession(t *testing.T) {
	row := t.Context()
	transport, stop := context.WithCancel(row)
	defer stop()
	local, peer := net.Pipe()
	defer peer.Close()
	mounted := make(chan struct{})
	reply := make(chan struct{})
	finished := make(chan error, 1)
	ends := 0
	go func() {
		done, err := openStream(transport, row, local, Sessions{Open: func(ctx context.Context, _ Far) (Near, error) {
			close(mounted)
			select {
			case <-ctx.Done():
				return Near{}, ctx.Err()
			case <-reply:
			}
			return Near{End: func(ctx context.Context, why CloseReason) error {
				if ctx.Err() != nil || why.Kind != "done" {
					t.Errorf("unexpected end: %v, %v", ctx.Err(), why)
				}
				ends++
				return nil
			}}, nil
		}})
		if err == nil {
			err = <-done
		}
		finished <- err
	}()
	<-mounted
	stop()
	peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := readFrame(peer); err != io.EOF {
		t.Fatalf("transport still open: %v", err)
	}
	close(reply)
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) || ends != 1 {
			t.Fatalf("result %v, ended %d times", err, ends)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late session did not finish")
	}
}
