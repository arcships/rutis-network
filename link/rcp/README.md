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

## Android and iOS

The [Kotlin module](kotlin) supports Android API 29+ and JVM 17. The [Swift package](swift) supports iOS 15+ and macOS 12+. Both implement RCP/1 and consume the same wire vector as Go. For Android, use JDK 17 and build the AAR with `cd link/rcp/kotlin && ./gradlew -Pandroid=true :android:assembleRelease` with an Android SDK installed. Use the Android Gradle module with its declared dependencies. On iOS, add `link/rcp/swift` as a local Swift package and select the `RutisRCP` product.

Start one Kotlin coroutine per device with `runRcp(provider = { freshGrant() }) { sender -> receiverFor(sender) }`. On Swift, start one task per device with `try await RCPLink.run(provider: { try await freshGrant() }) { sender in try await receiverFor(sender) }`. The app supplies both callbacks. The provider returns a grant using the fields above on each attempt. The second callback receives a sender after authenticated READY and returns a `Receiver` (Kotlin) or `RCPReceiver` (Swift). Await each send and receive callback for ordered delivery and backpressure. The receiver's end callback gets the close reason. A local close defaults to done. To stop the remote dialer on replacement or rejection, pass Kotlin `CloseReason.REPLACED` or `CloseReason.REJECTED`, or Swift `.replaced` or `.rejected`, to `sender.close`. A Kotlin cleanup callback failure preserves the link's original close and retry decision. Cancel the owning coroutine or task to unload the link. A local sender close also stops redial. The optional retry callback reports the public reason and delay in milliseconds.

Use the daemon's URL for a direct dial or the role-specific URL for a relay. Both dialers require WSS except on numeric loopback. An app that already accepts WebSockets can pass a binary socket to `serveRcp` or `RCPLink.serve`. Its adapter must pull messages on demand, check each frame length and cumulative fragmented message size before buffering the payload, and unblock pending I/O on abort. The read ceiling is 128 bytes until READY, then the negotiated message limit plus 33 bytes. An inbound limit violation or WebSocket close code 1009 reports `frame-too-large`. The mobile API exchanges opaque messages. Attaching them to a rutis mobile host for service calls requires the public SDK entry tracked in [rutis#1412](https://github.com/arcships/rutis/issues/1412).

From the repository root on macOS with JDK 17 and Xcode installed, run `make integration-rcp-mobile`. It builds both native-language test peers, checks the shared Go vector and exercises Go↔Kotlin, Go↔Swift and Kotlin↔Swift over direct and relayed WebSockets. Each peer sends a distinct message and checks the acknowledgement. Tests also cover both mobile daemon roles, rejection and retry, heartbeat loss, slow consumers, cancellation and exact message limits. The Android vector can run on an attached test device using `./gradlew -Pandroid=true :android:connectedDebugAndroidTest` from the Kotlin module.

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
