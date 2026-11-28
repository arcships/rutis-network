package rcp

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestDialBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		// The last attempt establishes a session after two waits at the cap.
		delays := []time.Duration{
			250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second,
			4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second,
		}
		var attempts atomic.Int32
		disconnect := make(chan struct{})
		stopped := make(chan error, 1)
		d := device{}
		go func() {
			stopped <- d.retryDial(ctx, func() (bool, error) {
				if int(attempts.Add(1)) == len(delays)+1 {
					select {
					case <-disconnect:
					case <-ctx.Done():
					}
					return true, transport
				}
				return false, unavailable
			})
		}()
		wantAttempts := func(want int) {
			t.Helper()
			synctest.Wait()
			if got := int(attempts.Load()); got != want {
				t.Fatalf("got %d attempts, want %d", got, want)
			}
		}
		waitForRetry := func(delay time.Duration, before int) {
			t.Helper()
			time.Sleep(delay - time.Nanosecond)
			wantAttempts(before)
			time.Sleep(time.Nanosecond)
			wantAttempts(before + 1)
		}
		wantAttempts(1)
		for i, delay := range delays {
			waitForRetry(delay, i+1)
		}
		time.Sleep(time.Minute)
		wantAttempts(len(delays) + 1) // no parallel dial while the session is live
		close(disconnect)
		wantAttempts(len(delays) + 1)
		waitForRetry(250*time.Millisecond, len(delays)+1)
		time.Sleep(100 * time.Millisecond) // cancel inside the next 500 ms wait
		cancel()
		wantAttempts(len(delays) + 2)
		select {
		case err := <-stopped:
			if err != nil {
				t.Fatalf("unload: %v", err)
			}
		default:
			t.Fatal("dial task did not exit on unload")
		}
		time.Sleep(time.Minute)
		wantAttempts(len(delays) + 2)
	})
}
