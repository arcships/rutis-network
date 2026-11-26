# Developing a connection plugin

[中文](link-plugins.md)

The plugins and examples pin rutis 0.23.0, session protocol 5 and plugin API 3. Upgrade the host and every language runtime on both endpoints together. A protocol 4 endpoint is rejected during handshake. [Migration guide](https://github.com/arcships/rutis/blob/v0.23.0/docs/migration-0.23.en.md)

| Component | Responsibility |
| --- | --- |
| Adapter | Reach the peer through a CLI, SDK or sidecar, check admission, manage heartbeat and reconnect, and hand over a fresh connection after each success. |
| Bridge | Preserve binary message boundaries and order, apply backpressure, hand each admitted connection to a session and handle closure and unload. |
| rutis session | Handshake, mount remote plugins and exchange services and events. The peer registers its ID in Hello. Imported plugin names have the form `<remote ID>:<plugin>`. |
| Application plugin | Consume services. A `<connection row>:<plugin>` row requests that the peer run a plugin. |

Admitting a peer grants trust in the identity it registers. Complete admission before handoff and close a denied connection. The adapter keeps certificates and tokens. A random port selects an address. Rust's Channel carries opaque messages with boundaries. Greeted provides the identity after handshake. [Channel](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/channel/mod.rs) · [Session and Greeted](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/session/plugin/mod.rs)

## Install the Go plugins

From the repository root, build the plugin binary with Go 1.24 or newer. Place it in `plugins/go` beside your `rutis.json` on each host. Only the build machine needs Go.

```sh
mkdir -p plugins/go
go -C link/tcp build -o ../../plugins/go/link-tcp ./cmd/link-tcp
```

Configure the dialing host with the listener’s port, its trusted CA and a client identity. The default is one connection attempt.

```json
{
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [{
    "id": "wire",
    "name": "go:link-tcp-dial",
    "config": {
      "port": 7443,
      "ca": "/path/to/peer-ca.pem",
      "cert": "/path/to/client.pem",
      "key": "/path/to/client.key"
    }
  }]
}
```

## Control a connection from a local management plugin

Add `"controlService": "office-tcp"` to the TCP row's `config` and `"inject": ["office-tcp"]` to the manager's row. **Set `"isolate": {"office-tcp": "management"}` on both rows** to keep the controls in the local management scope. Ordinary root-scope services are exported over sessions.

The manager uses `ctx.use('office-tcp')` to call `configure({host, port})`, `start()`, `stop()`, `status()` and `waitForChange(cursor)`. A managed dialer connects and retries automatically by default. It can load before a port is available. Updating the endpoint switches immediately while running. After an explicit stop, edits take effect on the next start. Reload restores deployment settings. [Complete deployment example and status fields](../design/06-link-tcp-integration.en.md)

## TCP byte-stream bridge

On the accepting host, use the same runtime configuration and a row named `go:link-tcp-listen`. Supply the server certificate and key and a CA dedicated to admitted clients. The listener binds to IPv4 loopback. The dialer accepts a DNS name, IPv4 address or IPv6 address in `host`. Port zero lets the listener choose and report a free port as `link/tcp listening 127.0.0.1:<port>`. Each verified client gets a separate session. Unloading the row closes the listener, pending TLS handshakes and admitted sockets. [Go adapters](../../link/tcp/adapter.go) · [Go bridge](../../link/tcp/bridge.go)

Run the checks below from the repository root. Integration tests use Node ≥22.18, npm, OpenSSL and a Unix host with control-socket support. They build the Go binary, start real rutis hosts and call a remote service through both the Go dialer and the single-dial TypeScript reference. The Rust example uses Cargo. [TypeScript reference](../../link/tcp/dial.ts) · [Host tests](../../link/tcp/test/hosts.test.mjs)

```sh
go -C link/tcp test -race ./...
npm ci --prefix link/tcp
npm --prefix link/tcp run typecheck
npm --prefix link/tcp test
cargo test --locked --manifest-path examples/rust-listen/Cargo.toml
```

Set `"reconnect": true` in the dial config for a persistent row. A port-only dialer verifies `localhost`. An explicit `host` defaults to verifying that name or IP. Set `servername` to the expected DNS name on the peer certificate when forwarding a different service to loopback. Each attempt rereads the key and certificates, including after rotation. A disconnect starts a fresh session and a fresh frame stream. A temporary failure prints, for example, `link/tcp 127.0.0.1:7443 ECONNREFUSED (retry in 500ms)`. Certificate rejection waits 30 seconds. Removing the row cancels the retry and releases its socket. See the [TCP design](../design/06-link-tcp.en.md) for the state and framing rules.

TCP accepts empty frames and carries up to 16 MiB per frame. On 0.23.0, applications can send a large value in successive chunks on one session and reassemble them in order. Leave room for encoding and metadata in each message. The integration example transfers 17 MiB using chunks of up to 4 MiB. Transparent chunking of a single large call depends on `sessions.open` exposing the frame limit. [Chunked echo example](../../link/tcp/test/fixtures/large.mjs) · [Upstream issue](https://github.com/arcships/rutis/issues/1411)

## Reverse tunnels

The service machine runs the rutis listener and the forwarding client. The relay runs the forwarding server and exposes a loopback port to the rutis dialer. Point the dial row at that forwarded port and keep verifying the service machine's TLS certificate. A tunnel credential controls tunnel creation. The application listener verifies each incoming client's certificate even when another local process reaches the forwarded port.

| Route | How to use it |
| --- | --- |
| frp | Run `frps -c link/tcp/tunnels/frps.toml` on the relay and `frpc -c link/tcp/tunnels/frpc.toml` on the service machine. Both configurations are runnable on one machine. Both processes need `FRP_CONTROL_PORT`, `LINK_FORWARD_PORT` and `FRP_TOKEN_FILE` (a private file containing the same random token on both sides). frps needs `FRP_CERT_FILE` and `FRP_KEY_FILE`; frpc needs `FRP_CA_FILE` and `LINK_LISTEN_PORT`. These examples bind the control and forwarded ports to loopback. For a separate relay, set the control bind address, client server address and expected TLS server name to the intended private route. Keep `proxyBindAddr` on loopback for a dialer on the relay. [frps](../../link/tcp/tunnels/frps.toml) · [frpc](../../link/tcp/tunnels/frpc.toml) |
| SSH remote forwarding | On the service machine run `link/tcp/tunnels/ssh-reverse.sh ~/.ssh/config relay 17443 7443`. Configure the relay alias, authorized user key and trusted SSH host key first. Leave `GatewayPorts no` on the SSH server. The dialer on the relay connects to port 17443, which forwards to the local listener on port 7443. Port zero requests a relay-assigned port. [OpenSSH wrapper](../../link/tcp/tunnels/ssh-reverse.sh) |
| rathole / ngrok | A rathole service maps its server bind address to the client listener using a service token and protected transport. An ngrok TCP endpoint uses an account token and provider access policy. A public endpoint makes the forwarded port reachable by external clients. Retain application-level mutual TLS and restrict the ingress. |

To run both real tunnel tests, put `frpc`, `frps` (tested with 0.72.0), `ssh` and `ssh-keygen` on PATH. The tests use `/usr/sbin/sshd` by default, or the executable in `SSHD`. The local user must be able to run a test daemon. No external accounts are required. Each test creates temporary keys and a loopback relay, starts two rutis hosts and verifies the reply to a remote service call. From the repository root:

```sh
npm --prefix link/tcp run test:tunnels
```

## Handoff in TypeScript and Python

Declare `rutis/sessions` in the plugin's `inject`. Calling `open(far)` mounts a child session and returns `near`. Messages can flow at this point. The handshake can still fail. Python uses the same callbacks. [Service implementation](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/session/plugin/open.rs)

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

The Node SDK invokes the cleanup function passed to `effect` directly at unload. Register it once. Cleanups run sequentially in reverse order. In 0.21 remote callbacks default to asynchronous. Await the write and the receive queue. The reference writes one frame at a time up to 16 MiB, including valid frames larger than 4 MiB. This bounds the bridge's in-flight write. The 0.23 `open(far)` service uses an unbounded session outbound queue. Deployments requiring an end-to-end memory bound should use the Rust API to set `outbound_limit`. [TCP bridge](../../link/tcp/bridge.ts)

## Rust listener and replacement

```rust
let view = ctx.plugin(Session::new(admitted_channel));
(&view).await?;
```

For replacement, call `Session::handshake(&ctx, admitted_channel).await?` and read `greeted.id()`. The example keeps the mounted handle by ID. After a successful greeting it marks the old connection for replacement and waits for the old session and its children to unload. The old channel closes with `Close::Replaced` before the new session mounts. A failed handshake leaves the previous session live. The example handles admitted channels serially. Concurrent listeners can handshake in parallel and serialize replacement by ID. To set an explicit outbound bound, use `Session::new(channel).options(options)` or `Session::handshake_with`. [Listener implementation](../../examples/rust-listen/src/lib.rs) · [WebSocket admission and dial example](../../examples/rust-listen/tests/loopback.rs)

The non-Rust 0.23 service returns only receive and end. The first session under a connection row registers its addressing handle. Listeners requiring same-ID replacement use Rust. Keep SSH principals, certificate subjects and session payloads out of the bridge's identity handling.

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
