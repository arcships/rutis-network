package tcp

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestDialTarget(t *testing.T) {
	port := 443
	for _, tc := range []struct{ host, override, address, tls string }{
		{"", "", "127.0.0.1:443", "localhost"},
		{"vpn.example", "", "vpn.example:443", "vpn.example"},
		{"::1", "", "[::1]:443", "::1"},
		{"fe80::1%lo0", "", "[fe80::1%lo0]:443", "fe80::1"},
		{"127.0.0.1", "vpn.example", "127.0.0.1:443", "vpn.example"},
	} {
		dest := target{Host: tc.host, ServerName: tc.override, Port: &port}
		if err := dest.validate(true); err != nil || dest.address() != tc.address || dest.tlsName() != tc.tls {
			t.Errorf("%+v: address %q, TLS name %q, error %v", tc, dest.address(), dest.tlsName(), err)
		}
	}
}

func TestInvalidPatchPreservesSnapshot(t *testing.T) {
	c := newControl(t.Context(), config{}, Sessions{}, true, settings{})
	err := c.Configure(map[string]any{"host": "localhost", "port": 443})
	if err != nil {
		t.Fatal(err)
	}
	before := c.snapshot()
	for _, patch := range []map[string]any{
		nil, {"port": nil}, {"port": "443"}, {"port": 2.5}, {"port": 0},
		{"host": "https://localhost"}, {"host": "fe80::1%bad\nzone"},
		{"host": "elsewhere", "servername": "[::1]"}, {"servername": "fe80::1%lo"},
		{"ca": "elsewhere"}, {"autoStart": false}, {"reconnect": "no"},
	} {
		if err := c.Configure(patch); err == nil || c.snapshot().Cursor != before.Cursor {
			t.Fatalf("unexpected change for %#v: %+v, %v", patch, c.snapshot(), err)
		}
	}
	// Go consumers in the same process also get an independent snapshot.
	*before.Config.Port = 0
	if *c.snapshot().Config.Port != 443 {
		t.Fatal("mutating a returned snapshot changed the configuration")
	}
}

func TestControlWaitCancellationAndConcurrentUpdates(t *testing.T) {
	row, unload := context.WithCancel(t.Context())
	defer unload()
	c := newControl(row, config{}, Sessions{}, true, settings{})
	initial := c.snapshot()
	cancelled, cancel := context.WithCancel(row)
	cancel()
	if _, err := c.WaitForChange(cancelled, initial.Cursor); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait returned %v", err)
	}
	var watchers sync.WaitGroup
	for range 12 {
		watchers.Add(1)
		go func() {
			defer watchers.Done()
			s, err := c.WaitForChange(row, initial.Cursor)
			if err != nil || s.Config.Port == nil || s.Cursor == initial.Cursor || s.Desired != "stopped" {
				t.Errorf("wait returned %+v, %v", s, err)
			}
		}()
	}
	var writers sync.WaitGroup
	for port := 1; port <= 6; port++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			if err := c.Configure(map[string]any{"port": port}); err != nil {
				t.Error(err)
			}
		}()
	}
	writers.Wait()
	watchers.Wait()
	cursor := c.snapshot().Cursor
	unload()
	if _, err := c.WaitForChange(context.Background(), cursor); !errors.Is(err, context.Canceled) {
		t.Fatalf("row unload returned %v", err)
	}
}
