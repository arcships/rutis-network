package webrtclink

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPPollingAndICEURLForms(t *testing.T) {
	t.Setenv("PAIR_TOKEN", "test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/receive" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		fmt.Fprint(w, `{"iceServers":[{"urls":"turn:relay.test","username":"a","credential":"b"},{"urls":["stun:a.test","stun:b.test"]}]}`)
	}))
	defer server.Close()
	provider := httpProvider{base: server.URL, tokenEnv: "PAIR_TOKEN", client: server.Client()}
	d, err := provider.Receive(context.Background(), nil)
	if d != nil || err != nil {
		t.Fatalf("empty poll: %v %v", d, err)
	}
	g, err := provider.Acquire(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.IceServers) != 2 || len(g.IceServers[0].URLs) != 1 || g.IceServers[0].URLs[0] != "turn:relay.test" || g.IceServers[0].Credential != "b" || len(g.IceServers[1].URLs) != 2 {
		t.Fatal("lost ICE configuration")
	}
}
