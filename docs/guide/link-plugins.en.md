# Developing a connection plugin

[中文](link-plugins.md)

The runnable examples pin rutis 0.21.0, session protocol 4 and plugin API 3. Use the same session protocol on both endpoints. A 0.23.0 endpoint uses protocol 5 and refuses a protocol 4 peer during handshake. [Migration guide](https://github.com/arcships/rutis/blob/v0.23.0/docs/migration-0.23.en.md)

| Component | Responsibility |
| --- | --- |
| Adapter | Reach the peer through a CLI, SDK or sidecar, check admission, manage heartbeat and reconnect, and hand over a fresh connection after each success. |
| Bridge | Preserve binary message boundaries and order, apply backpressure, hand each admitted connection to a session and handle closure and unload. |
| rutis session | Handshake, mount remote plugins and exchange services and events. The peer registers its ID in Hello. Imported plugin names have the form `<remote ID>:<plugin>`. |
| Application plugin | Consume services. A `<connection row>:<plugin>` row requests that the peer run a plugin. |

Admitting a peer grants trust in the identity it registers. Complete admission before handoff and close a denied connection. The adapter keeps certificates and tokens. A random port selects an address. Rust's Channel carries opaque messages with boundaries. Greeted provides the identity after handshake. [Channel](https://github.com/arcships/rutis/blob/v0.21.0/crates/rutis-bridge/src/channel/mod.rs) · [Session and Greeted](https://github.com/arcships/rutis/blob/v0.21.0/crates/rutis-bridge/src/session/plugin/mod.rs)

## Run the reference implementation

Run these commands from the repository root. The Node example uses Node ≥22.18, npm, OpenSSL and a Unix system supporting the rutis-host control socket. The Rust example uses Cargo.

```sh
npm ci --prefix link/tcp
npm --prefix link/tcp run typecheck
npm --prefix link/tcp test
cargo test --locked --manifest-path examples/rust-listen/Cargo.toml
```

The Node test generates temporary credentials and starts two hosts on `127.0.0.1`. The dialer verifies the server certificate and hostname. The listener verifies the client certificate. Only locally authenticated sockets reach the bridge. A call to the remote `witness.echo` returns a value unique to this run. The Rust test admits three connections over the existing WebSocket transport and checks replacement of one ID while another stays connected. [TS dial plugin](../../link/tcp/dial.ts) · [Two-host test](../../link/tcp/test/hosts.test.mjs) · [Rust listener example](../../examples/rust-listen/tests/loopback.rs)

For a single dial, enable the Node runtime and add the following row to `rutis.json`. Set the port to the local TLS listener and use its trusted CA. The server certificate must cover `localhost`. The listener must require an authorized client certificate and speak the same framing.

```json
{
  "id": "wire",
  "name": "/absolute/path/to/rutis-network/link/tcp/dial.ts",
  "config": {
    "port": 7443,
    "ca": "/path/to/peer-ca.pem",
    "cert": "/path/to/client.pem",
    "key": "/path/to/client.key"
  }
}
```

## Handoff in TypeScript and Python

Declare `rutis/sessions` in the plugin's `inject`. Calling `open(far)` mounts a child session and returns `near`. Messages can flow at this point. The handshake can still fail. Python uses the same callbacks. [Service implementation](https://github.com/arcships/rutis/blob/v0.21.0/crates/rutis-bridge/src/session/plugin/open.rs)

| Direction | Call and completion |
| --- | --- |
| Session to network | The session waits for `far.send(bytes)` before sending again. `far.close(why)` releases the connection. |
| Network to session | Await `near.receive(bytes)` for each complete message to enter the queue. Call `near.end(why)` once when the network ends. |

Once the adapter has verified the socket, its plugin can attach it in `apply`:

```ts
ctx.effect(() => { socket.destroy() })
const link = await openTcp(socket, ctx.use('rutis/sessions'))
link.done.catch(reportTransportError)
```

The Node SDK invokes the cleanup function passed to `effect` directly at unload. Register it once. Cleanups run sequentially in reverse order. In 0.21 remote callbacks default to asynchronous. Await the write and the receive queue. The reference writes one frame at a time up to 16 MiB, including valid frames larger than 4 MiB. This bounds the bridge's in-flight write. The 0.21 `open(far)` service uses an unbounded session outbound queue. Deployments requiring an end-to-end memory bound should use the Rust API to set `outbound_limit`. [TCP bridge](../../link/tcp/bridge.ts)

## Rust listener and replacement

```rust
let view = ctx.plugin(Session::new(admitted_channel));
(&view).await?;
```

For replacement, call `Session::handshake(&ctx, admitted_channel).await?` and read `greeted.id()`. The example keeps the mounted handle by ID. After a successful greeting it marks the old connection for replacement and waits for the old session and its children to unload. The old channel closes with `Close::Replaced` before the new session mounts. A failed handshake leaves the previous session live. The example handles admitted channels serially. Concurrent listeners can handshake in parallel and serialize replacement by ID. To set an explicit outbound bound, use `Session::new(channel).options(options)` or `Session::handshake_with`. [Listener implementation](../../examples/rust-listen/src/lib.rs) · [WebSocket admission and dial example](../../examples/rust-listen/tests/loopback.rs)

The non-Rust 0.21 service returns only receive and end. The first session under a connection row registers its addressing handle. Listeners requiring same-ID replacement use Rust. Keep SSH principals, certificate subjects and session payloads out of the bridge's identity handling.

## Transport and failure conformance

| Condition | Observable behavior |
| --- | --- |
| Short reads, coalesced messages, empty frames | Read a 4-byte big-endian length and exactly that many bytes. Deliver complete messages in order. A newline-framed pipe uses the format agreed with its peer. |
| Oversize or truncated input | Reject lengths over 16 MiB with `frame-too-large` before allocating the payload. EOF within a frame fails with `truncated-frame`. Both close the connection and release the session. |
| Slow peer | The send promise settles when the socket write finishes. The reader awaits receive before handing over the next message. A new connection starts a fresh frame stream without replay. |
| End or unload | EOF at a frame boundary calls `end({kind:'done'})`. Ending either direction closes the entire channel. Session or parent unload closes the socket. Repeated cleanup has no side effects. |
| Admission, naming and replacement | Denied admission creates zero sessions. The session names remote plugins by Hello ID. An admitted connection with a failed handshake leaves the old session live. Same-ID replacement affects only the old session for that ID. |
| Reconnect | The adapter retries from 0.5 seconds, doubling up to 30 seconds and resetting after 60 seconds connected. A known admission rejection retries every 30 seconds. A known protocol mismatch stops. Non-Rust adapters lack a structured session handshake result and use transport backoff for unknown disconnects. |

The rutis close reasons are `done`, `replaced` and `rejected` with a reason. TCP carries EOF. WebSocket can carry 4002 or 4003. Diagnostics identify the connection row, address and error code. Credentials stay in the adapter. Match rutis errors by `code` and `data`, for example `not-declared` with `data.service`. The single-dial example prints transport errors as `link/tcp 127.0.0.1:<port> <code>`. The cross-language suite is tracked in [issue #10](https://github.com/arcships/rutis-network/issues/10).
