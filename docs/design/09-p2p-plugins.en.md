# Go libp2p link plugin

[中文](09-p2p-plugins.md)

**Proposal.** Ship one go-libp2p binary providing `go:link-libp2p`. Each row connects to one peer. The dialer pins the Peer ID at the end of the address. The listener accepts only the authenticated Peer ID in `allowPeer`. Open a bidirectional stream for `/rutis/link/1` and hand it to `rutis/sessions.open(far)` after admission.

## Configure both ends

Place the plugin in `plugins/go` and set `"runtimes": {"go": {"dir": "plugins/go"}}` in each host's `rutis.json`. Provision a persistent key for each host and exchange the public Peer IDs. Add the dial row to A's `rows`, replacing the example address with B's reachable address.

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

The default path is direct QUIC. For a relay, set B's optional `relay` to the relay's full multiaddr so B can reserve a slot. Set A's `dial` to `<relay multiaddr>/p2p-circuit/p2p/<B_PEER_ID>`. [Go relay example](https://github.com/libp2p/go-libp2p/blob/v0.52.0/examples/relay/main.go)

## Control the instance

The row starts when loaded. A management plugin injects `office-link` with the matching `"isolate": {"office-link": "local"}`. The first version exposes three methods. Edit and reload the row to change an address or key.

| Method | Behavior |
| --- | --- |
| `start()` | Dial or listen using the configured address. Safe to repeat. |
| `stop()` | Cancel pending dials and close the row's listener and session. The control service stays available for restart. |
| `status()` | Return the current state, connected Peer ID, observed direct or relay path and most recent error. |

One stream carries one session using a four-byte big-endian length prefix and a 16 MiB frame limit. The rutis 0.23 Go API exposes neither the rutis ID learned in the handshake nor a session outbound queue limit. Each row admits one active stream and accepts a new one once the old stream ends. [Session API](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/session/plugin/open.rs)

A local probe verified a framed round trip between two Go nodes over Circuit Relay v2. Mounting the plugin in two hosts and exercising its control service remain implementation checks.
