package mdns

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arcships/rutis/go/rutis/rutistest"
)

// Set MDNS_TEST_INTERFACE to a multicast interface to run a real DNS-SD
// registration, observation and withdrawal through two independent rutis rows.
func TestLiveRows(t *testing.T) {
	iface := os.Getenv("MDNS_TEST_INTERFACE")
	if iface == "" {
		t.Skip("set MDNS_TEST_INTERFACE to run the live multicast test")
	}
	kind := "_rt-" + strings.ToLower(rand.Text()[:8]) + "._tcp"
	instance := "Test.节点 " + rand.Text()[:8]
	browse := rutistest.Load(t, Plugin, map[string]any{"interface": iface, "service": "observed", "browse": kind}, nil, rutistest.Row("browse-local"))
	svc := browse.Service("observed")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	var state Snapshot
	if err := svc.Call(ctx, "snapshot", &state); err != nil {
		t.Fatal(err)
	}
	if state.Cursor == "" || len(state.Candidates) != 0 {
		t.Fatalf("initial Snapshot: %+v", state)
	}
	publisher := rutistest.Load(t, Plugin, map[string]any{"interface": iface, "publish": map[string]any{"type": kind, "instance": instance, "port": 17443}}, nil)
	for len(state.Candidates) == 0 {
		if err := svc.Call(ctx, "waitForChange", &state, state.Cursor); err != nil {
			t.Fatalf("waiting for announcement: %v", err)
		}
	}
	got := state.Candidates[0]
	if got.Source != "browse-local" || got.Interface != iface || got.Instance != instance || got.Port != 17443 || got.Transport != "tcp" || len(got.Addresses) == 0 {
		t.Fatalf("unexpected candidate: %+v", got)
	}
	publisher.Unload()
	for len(state.Candidates) != 0 {
		if err := svc.Call(ctx, "waitForChange", &state, state.Cursor); err != nil {
			t.Fatalf("waiting for withdrawal: %v", err)
		}
	}
}
