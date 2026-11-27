package tcpoffer

import (
	"encoding/json"
	"net/netip"
	"testing"
	"time"

	"github.com/arcships/rutis-network/discovery/mdns"
)

func TestOnlyDialUniqueLocallyAdmittedOffer(t *testing.T) {
	now := time.Now()
	cfg := dialConfig{ID: "office", ServerName: "office.example"}
	permitted := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("127.0.0.0/8")}
	candidate := func(p string) mdns.Candidate {
		return mdns.Candidate{Interface: "nonexistent-interface", ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano), Port: 9000, Description: &mdns.Description{ID: "office", Kind: "rutis.tcp", Version: 1, Params: json.RawMessage(p)}}
	}
	good := candidate(`{"host":"192.0.2.2","port":443}`)
	if target, ok := cfg.target([]mdns.Candidate{good}, permitted, now); !ok || target.String() != "192.0.2.2:443" {
		t.Fatal(target, ok)
	}
	expired := good
	expired.ExpiresAt = now.Format(time.RFC3339Nano)
	for _, list := range [][]mdns.Candidate{
		{good, good}, {expired}, {candidate(`{"host":"192.0.2.2","port":443,"serverName":"other.example"}`)},
		{candidate(`{"host":"192.0.2.2","port":0}`)}, {candidate(`{"host":"198.51.100.2","port":443}`)},
		{candidate(`{"host":"127.0.0.1","port":443}`)}, {candidate(`{"host":"untrusted.example","port":443}`)},
	} {
		if target, ok := cfg.target(list, permitted, now); ok {
			t.Fatalf("accepted %s", target)
		}
	}
}
