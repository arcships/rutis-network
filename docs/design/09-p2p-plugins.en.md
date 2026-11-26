# Go libp2p link plugin

[中文](09-p2p-plugins.md)

This plugin uses go-libp2p 0.52.0 with rutis-host 0.23.0. A single binary provides `go:link-libp2p`. Each row connects to one peer. The dialer pins the Peer ID at the end of the address. The listener accepts only the authenticated Peer ID in `allowPeer`. Open a bidirectional stream for `/rutis/link/1` and hand it to `rutis/sessions.open(far)` after admission.

## Configure both ends

Build from the repository root with Go 1.26 or later. Run `keygen` once on each host. It creates an owner-only private key file and prints the public Peer ID. It refuses to overwrite an existing file.

```sh
(cd link/libp2p && go build -o ../../plugins/go/link-libp2p ./cmd/link-libp2p)
./plugins/go/link-libp2p keygen ./peer.key
```

Place the plugin in `plugins/go` and set `"runtimes": {"go": {"dir": "plugins/go"}}` in each host's `rutis.json`. Set each row’s `keyFile` to its generated key’s absolute path and exchange the public Peer IDs. Add the dial row to A's `rows`, replacing the example address with B's reachable address.

```json
{
  "id": "to-b",
  "name": "go:link-libp2p",
  "isolate": { "office-link": "local" },
  "config": {
    "keyFile": "/etc/rutis/a.key",
    "dial": "/ip4/192.0.2.10/udp/7444/quic-v1/p2p/<B_PEER_ID>",
    "controlService": "office-link"
  }
}
```

Add the listener row to B's `rows`.

```json
{
  "id": "from-a",
  "name": "go:link-libp2p",
  "config": {
    "keyFile": "/etc/rutis/b.key",
    "listen": "/ip4/0.0.0.0/udp/7444/quic-v1",
    "allowPeer": "<A_PEER_ID>"
  }
}
```

The default path is direct QUIC. For a relay, set B's `relay` to the relay's full multiaddr. B reserves a slot and renews it automatically. To coordinate the first dial, enable controls on B and wait for `status().relayReady`. Take the address ending in `/p2p-circuit` from `listenAddrs`, append `/p2p/<B_PEER_ID>` and use it as A's `dial`. If the reservation expires or the relay disconnects, B clears readiness and retries. [Go relay example](https://github.com/libp2p/go-libp2p/blob/v0.52.0/examples/relay/main.go)

## Control the instance

The row starts when loaded. A listener that cannot bind its address fails to load with the bind error. Each dial makes one attempt. Call `start()` again after a disconnect. The listener keeps accepting new streams from its configured peer. Edit and reload the row to change an address or key.

A management plugin injects `office-link` with the same `"isolate": {"office-link": "local"}` as the link row. Set this entry directly on the link row. rutis-host 0.23 passes the row's own isolation label to Go for a dynamically named service. A missing or misspelled entry fails the load before controls are published.

| Method | Behavior |
| --- | --- |
| `start()` | Dial or listen using the configured address. Safe to repeat. |
| `stop()` | Cancel pending dials and close the row's listener and session. The control service stays available for restart. |
| `status()` | Return the current state, connected Peer ID, observed direct or relay path and most recent error. |

One stream carries one session using a four-byte big-endian length prefix and a 16 MiB frame limit. The rutis 0.23 Go API exposes neither the rutis ID learned in the handshake nor a session outbound queue limit. Each row admits one active stream and accepts a new one once the old stream ends. [Session API](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/session/plugin/open.rs)

Run `(cd link/libp2p && go test -race ./...)` for local QUIC and relay round trips, unauthorized and duplicate streams, framing and restart. With rutis-host 0.23.0 on PATH or selected by `RUTIS_HOST`, run `(cd link/libp2p && go test -tags rutishost -run TestTwoHosts)` to check remote service calls, stop, restart and control isolation in two real hosts.
