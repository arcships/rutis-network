package webrtclink

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/arcships/rutis-network/link/tcp"
)

func TestRecordBoundaries(t *testing.T) {
	newReceiver := func() *wire { return &wire{ctx: context.Background(), messages: make(chan []byte, 1)} }
	for _, n := range []int{0, 1, recordLimit - 5, recordLimit, messageLimit} {
		w := newReceiver()
		header := make([]byte, 5+min(n, recordLimit-5))
		header[0] = 1
		binary.BigEndian.PutUint32(header[1:5], uint32(n))
		if !w.take(header) {
			t.Fatalf("rejected first record at %d", n)
		}
		for left := n - (len(header) - 5); left > 0; {
			record := make([]byte, 1+min(left, recordLimit-1))
			record[0] = 2
			if !w.take(record) {
				t.Fatalf("rejected continuation at %d", left)
			}
			left -= len(record) - 1
		}
		if b := <-w.messages; len(b) != n {
			t.Fatalf("got %d, want %d", len(b), n)
		}
		if w.take([]byte{1, 0, 0, 0, 0}) {
			t.Fatal("accepted next message before delivery")
		}
	}
	invalid := [][]byte{nil, {3}, {2, 0}, {1, 0, 0, 0, 1}, {1, 0, 0, 0, 0, 8}, {1, 1, 0, 0, 1, 0}, make([]byte, recordLimit+1)}
	for _, record := range invalid {
		if newReceiver().take(record) {
			t.Fatalf("accepted invalid record of size %d", len(record))
		}
	}
}

func TestStopWhileSessionIsOpening(t *testing.T) {
	row := t.Context()
	ctx, cancel := context.WithCancelCause(row)
	w := &wire{ctx: ctx, cancel: cancel}
	entered, release, ended := make(chan struct{}), make(chan struct{}), make(chan struct{})
	sessions := tcp.Sessions{Open: func(_ context.Context, _ tcp.Far) (tcp.Near, error) {
		close(entered)
		<-release
		return tcp.Near{End: func(_ context.Context, _ tcp.CloseReason) error {
			close(ended)
			return nil
		}}, nil
	}}
	done := make(chan error, 1)
	go func() { done <- w.serve(row, sessions, recordLimit, func() { t.Error("mounted after stop") }) }()
	<-entered
	cancel(errClosed)
	select {
	case err := <-done:
		if err != errClosed {
			t.Fatalf("got %v after stop", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop blocked on the host mount")
	}
	close(release)
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("late session was not ended")
	}
}
