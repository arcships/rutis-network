package webrtclink

import (
	"context"
	"encoding/binary"
	"testing"
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
