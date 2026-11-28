# RCP device link

[简体中文](README.zh-CN.md)

`go:link-rcp` connects one rutis device per configuration row. Both ends dial an opaque WebSocket relay, or a client dials a daemon listener directly. The Go runtime shares the plugin process across rows. Each row owns its socket, session, credential callback and retry loop. The host starts and stops the plugin process. Removing a row drains its connection. To revoke an active device, remove its row and revoke the grant at the issuer.

## Configure a host

Build `go build -o /path/to/project/plugins/go/link-rcp ./cmd/link-rcp` from this directory. Register the Go runtime directory and one row in the project's `rutis.json`.

```json
{
  "id": "desk",
  "runtimes": { "go": { "dir": "./plugins/go" } },
  "rows": [
    {
      "id": "tablet",
      "name": "go:link-rcp",
      "config": { "linkId": "tablet", "credentialFile": "/run/rcp/tablet.json" }
    }
  ]
}
```

The standalone binary rereads the caller's credential file on every connection attempt. Use an atomically replaced regular file, accessible only to its owner (0600 on Unix, equivalent ACL on Windows). Account systems embedding the Go definition can pass their own `Provider` to `rcp.Plugin`. The callback runs locally and receives the row's `linkId`. Network timeouts use transport backoff. Wrap `rcp.ErrProviderUnavailable` for other temporary issuer errors (for example, an HTTP 503). A direct listener returns HTTP 503 during such an outage. Other provider errors reject the grant.

| Credential field | Value supplied by the caller |
| --- | --- |
| `role` | `client` initiates pairing, `daemon` responds |
| `address` | Dial URL, including any short-lived routing ticket required by the relay. Optional on direct listeners. WSS uses system trust roots. Plain WS requires a numeric loopback address. |
| `privateKey`, `peerKey` | Base64-encoded 32-byte local X25519 private key and independently pinned remote public key |
| `binding` | Base64-encoded nonempty byte string shared by the pair, up to 1024 bytes. The issuer binds it to the current grant. |
| `expiresAt` | RFC 3339 admission expiry. A grant must be current at completion of pairing. Existing sessions follow the row lifecycle. |

For direct connections add `"listen": "wss://0.0.0.0:7443/pair"` and TLS certificate and key file paths (`cert`, `key`) to the daemon row. The client grant supplies the corresponding dialable URL. Local testing can use `ws://127.0.0.1:7443/pair`. In relay mode both roles dial the address from their own grant. The credential issuer supplies the complete URL for each role. Relay deployments must allow the configured message size plus 33 bytes per encrypted record and the 60-byte handshake messages.

`maxMessage` defaults to 16 MiB of rutis message bytes. `heartbeatMs` defaults to 10000. See the wire specification for negotiation. The link serializes socket writes and awaits each session receive. Rutis 0.23.0 owns the separate session writer queue and its Go opening API cannot bound that queue or pass the negotiated frame limit to the chunker. On this host an oversized RPC ends the connection with `frame-too-large`. The integration test returns 17 MiB from each direction, then verifies that small calls recover and the second device stays online. [Upstream frame-limit issue](https://github.com/arcships/rutis/issues/1411)

The diagnostic fields are the operator-supplied link ID, direction, stage and a fixed reason code. Keep link IDs suitable for logs. Transport failures, peer restarts, oversized messages and relay or provider outages trigger another attempt after 250 ms, doubling up to 30 s. A credential refusal or expired grant allows one refresh attempt. Version mismatch, malformed frames, replacement and session refusal stop the dial task. A local session close stops redial even when the peer cannot receive its final record. Reload the row after correcting a terminal error. Every successful pairing resets the backoff.

## Verify

From this directory run `go test -race ./...` for vectors, admission, relay fault mapping, message order, limits, backpressure and refresh. From the repository root run `npm ci --prefix link/tcp/node` and `make integration-rcp` for three real host processes with two device rows, a direct connection and a local forwarding relay. The test creates its own grants. It runs service calls in both directions and examines captured relay records.

Run `make integration-rcp-direct` or `make integration-rcp-relay` from the repository root for the Go fault matrix. CI reports each path in a separate step. To run one case from this directory, use `go test -race -v -run '^TestGoFaultMatrix$/^relay$/^peer_offline$' .`.

| Fault case | Expected dial behavior |
| --- | --- |
| `version_mismatch` | Stop on version rejection without opening a session. |
| `invalid_grant`, `revoked_grant` | Refresh once after a binding mismatch or HTTP 403. A second rejection stops the row's dial task. |
| `unavailable_503` | Retry after HTTP 503 and exchange a message on the new session. |
| `peer_offline` | A closed direct endpoint reports `transport-error`. Relay HTTP 404 reports `peer-offline`. Both retry and exchange a message after recovery. |

`go test -race -run '^TestDialBackoff$' .` uses virtual time to check each retry interval from 250 ms to the 30 s cap, the reset after a live session, and cancellation during a pending retry. `make integration-rcp-hosts` also unloads a real configuration row during backoff and checks that credential requests stop.

[Wire specification and vectors](wire.md)
