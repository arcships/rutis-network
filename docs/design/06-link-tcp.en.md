# `link/tcp` generic byte-stream bridge: design

Status: draft. Date: 2026-11-08. Baseline: rutis 0.21.0.
Issue: [#6](https://github.com/arcships/rutis-network/issues/6). Depends on [#1](https://github.com/arcships/rutis-network/issues/1).

## 1. Goal

Attach any adapter that gives a TCP byte stream (SSH forwarding, Tailscale, Cloudflare access, ADB, usbmuxd, frp/rathole, ngrok) to a rutis session, writing framing, backpressure, close and lifecycle once.

`link/tcp` does **not** authenticate; the adapter below it does.

## 2. Boundary

```text
adapter (SSH/Tailscale/Cloudflare/ADB/frp) ── TCP bytes ── link/tcp bridge ── rutis/sessions
```

- The bridge does not know the far end, does not parse the session protocol, and does not admit.
- One TCP connection = one bridge instance = one session.
- A reverse tunnel only brings local/public TCP; it does not repeat the bridge.

### 2.1 localhost TCP is the first adapter

- Start with `dial 127.0.0.1:<port>` / `listen 127.0.0.1:0`, the default transport for the conformance suite and local development.
- Bind `127.0.0.1` only. A random port is not admission: any local process can find it. Admit with Unix-socket permissions, or define a token frame before this bridge; raw TCP loopback with no admission is for development and tests only.
- It adds no session logic; it is the first adapter and test base for the #6 bridge.

### 2.2 WebSocket already exists; HTTP is not a link

- The core already has `websocket::dial` / `websocket::listen`, and `rutis-host/listen` uses it; this bridge does not rewrite WebSocket.
- Plain HTTP request/response has no duplex long-lived stream or message boundaries; it is not a link. HTTP only carries a WebSocket upgrade or gRPC.

## 3. Frame format

```text
+----------------+--------------------------+
| length: u32 BE | payload: length bytes    |
+----------------+--------------------------+
```

- `length` is the payload size, excluding the 4-byte header.
- Maximum 16 MiB (`16 * 1024 * 1024`); reject `length > 16 MiB` before allocating, then close.
- `length == 0` is allowed: an empty message still has a boundary.
- No newline framing, no compression, no encryption (TLS/SSH/Tailscale live in the adapter).
- Why 4 bytes: 16 MiB is enough and 4 bytes is smaller than 8; a larger frame needs a versioned extension, not a mixed format.

## 4. Sending

- Send one message: write the 4-byte big-endian length, then the payload; handle partial writes (`write` returning less than requested).
- The send queue is bounded (default 64 messages or 4 MiB, whichever comes first; this is bridge policy, not a `Channel` contract); when full, `send` waits.
- Since 0.21, `far.send` may be async: the session awaits it; write blocking applies backpressure through the promise. Do not fire-and-forget.
- Since 0.20, a session has a writer thread and its outbound queue is unbounded by default. Rust uses `Session::options(SessionOptions { outbound_limit: Some(bytes) })`; non-Rust `open(far)` has no such option, and a bridge-side queue cannot bound the session queue (O2). Do not present "the bridge is bounded" as "the session is bounded".
- On write failure/connection close, fail the current `send`, close the connection, and `near.end`.

## 5. Receiving

- Loop: read exactly 4 header bytes, then exactly `length` payload bytes; handle short reads and coalesced frames.
- When a full payload arrives, call `near.receive(payload)`; it returns a promise and waits when the session queue (64 messages) is full — that is receive backpressure.
- Over-limit `length`: close and report; do not allocate 16 MiB before checking.
- EOF mid-frame means truncation: report and close. EOF on a frame boundary: `near.end({kind:'done'})`.

## 6. Close and EOF

The session has no half-close, only `near.end({kind})`; do not design a half-close protocol.

- Far FIN (read EOF) → `near.end({kind:'done'})`, then close both directions.
- Session unload → close the socket write side, drain where possible, then close; once only.
- `near.end({kind:'replaced'})`: a same-id session took over; map to 4002 where the transport has a close code.
- `near.end({kind:'rejected', reason})`: refused after admission; map to 4003 where available.
- EOF in one direction ends the whole connection; do not wait for the other direction to send FIN, and do not close twice.

## 7. Listen side

- Accept each connection, create one bridge and one session.
- Rust: `Session::handshake` gives `Greeted::id()`; a newer connection of the same id unloads the old session, then mounts the new one; the old connection closes with `Close::Replaced` (4002).
- Other languages: `open(far)` does not expose the id, so same-id replacement is impossible. `session_key(row)` is first-wins: a second session under the same row registers no handle, and `<row>:<plugin>` only addresses the first. Do not write "coexist". A listener that needs replacement must be Rust (O1).
- Accept failures (EMFILE and friends) back off; no busy loop.

## 8. Reconnect

- The bridge does not reconnect; the adapter does (SSH/Tailscale/Cloudflare/frp).
- Suggested backoff: 0.5 s → 30 s exponential; no retry on auth failure; stop on protocol mismatch.
- A new connection = a new bridge = a new session; never reuse the old session.

## 9. Reverse-tunnel wrappers

| Tool | Gives | Admission | Notes |
| --- | --- | --- | --- |
| frp | local/public TCP | frpc token/TLS | Needs a VPS; public-port risk |
| rathole | local/public TCP | token/TLS | Lightweight; document config |
| ngrok | public TCP | authtoken | Temporary URL/rate limits; public risk |
| SSH `-R` | far forwarding to local | SSH host key/user key | Reuse link/ssh admission; do not authenticate twice |
| Cloudflare access | local TCP | Access token | See #5 |

A wrapper is an adapter/config, not a new crate; the bridge is unchanged.

## 10. Reference layout

```text
link/tcp/
  bridge.ts        # framing + open(far) + lifecycle
  listen.ts        # accept + one bridge per connection (Rust replacement)
  adapter.ts       # dial(host, port) / listen(host, port)
  errors.ts        # code + structured data
  README.md        # configuration, errors, limits
```

Rust uses `Channel` + `Session::new`; TS/Python use `rutis/sessions`.

## 11. Tests

- localhost TCP dial/listen works, the default test transport; bind 127.0.0.1 only; raw TCP without admission is for tests, production uses Unix-socket permissions or a token frame.
- Local TCP echo: single frame, empty frame, consecutive frames, coalesced/short reads, 16 MiB, 16 MiB + 1 (16 MiB is bridge policy).
- Order: 1000 small messages stay ordered.
- Backpressure: slow receiver + fast sender; the bridge queue stays within its bound; Rust verifies `SessionOptions::outbound_limit` ends the session instead of growing without bound.
- Async `send`: returns a promise, the session awaits it, a slow write loses nothing.
- Close/EOF: far FIN → `near.end`; session unload → socket closed; once; no half-close protocol.
- Listen: multiple connections, each a session; Rust same-id replacement; other languages explicitly unsupported.
- Reverse tunnel: frp or rathole runs; SSH `-R` or ngrok runs at least once; the bridge code does not change.
- Errors: truncated frame, over limit, connection reset; clear error with a code.
- No public network; CI uses loopback.

## 12. Non-goals

- No authentication or identity.
- No reliable UDP/QUIC; that is `link/quic`/P2P.
- No frp/rathole/ngrok server.
- No compression/encryption/multiplexing.
- Do not rewrite WebSocket (the core has it); plain HTTP request/response is not a link.
- No `link/tcp` core trait.

## 13. Open questions

- O1 (closed): the send queue is bounded by 64 messages / 4 MiB, first hit wins.
- O2 (closed): the frame length is fixed at 4 bytes; 8 bytes is a future extension.
- O3 (closed): the session has no half-close; EOF maps to `near.end({kind:'done'})` and closes both directions.
- O4 (blocking): non-Rust `open(far)` cannot set `outbound_limit`, and the session outbound queue is unbounded; production non-Rust links need rutis to add the option or a transport that applies backpressure.
- O5 (blocking): non-Rust cannot replace by `Hello` id; `session_key(row)` is first-wins, so a listener that needs replacement must be Rust.

## Links

- Issue [#6](https://github.com/arcships/rutis-network/issues/6)
- [Contract design](01-link-plugin-contract.en.md)
- [SSH design](03-link-ssh.en.md)
- [Tailscale design](04-link-tailscale.en.md)
- [Cloudflare design](05-link-cloudflared.en.md)
- rutis `crates/rutis-bridge/src/channel/mod.rs`
