# Link plugin API

This file is the part of the rutis plugin API a link plugin needs, written against rutis **0.21.0** (2026-11-05): session protocol 4, `rutis.json` format 1, plugin API 3. The rutis repository is authoritative; check `docs/guide/plugin-api.md` ("Your own connection") and `crates/rutis-bridge/src/session/plugin/open.rs` when the host changes.

## The contract

A link plugin gets a bidirectional, ordered, reliable stream of messages. It admits the connection (its own authentication), frames bytes into messages if needed, and hands the stream to the session layer.

```text
link plugin (any language)
  admission / reachability / framing / lifecycle
        │
        │ trusted far (messages in, messages out)
        ▼
session: open(far) → Near
  session lifecycle == channel lifecycle
  far end registers its id in Hello
```

- The link plugin owns **admission** and **transport**.
- The far end owns **its id** (session `Hello`).
- The session owns **plugins, services, events, lifecycle**.
- The session has no credentials and no authentication.

## Rust

```rust
use rutis_bridge::Session;

let channel = adapter.connect(&config).await?; // Channel
let session = ctx.plugin(Session::new(channel));
```

`Session::new(channel)` handshakes and mounts in one plugin. If you need the far end's id before mounting, use:

```rust
let greeted: Greeted = rutis_bridge::Session::handshake(&ctx, channel).await?;
println!("{}", greeted.id());
let session = ctx.plugin(greeted);
```

The host provides `LocalId` at the root context (`rutis-host` does this from `rutis.json`'s `id`). There is no default: a session without one fails with `the host provides no id for its sessions`.

`Channel` is the 0.19 transport contract: sender, receiver, closer; ordered, reliable, keeps message boundaries. `pipe::framed(read, write, closer)` frames a byte stream as newline-delimited messages (16 MiB limit, no raw newline in a message). For a new adapter, length-prefixed framing is preferred; newline framing is valid when the far end is `pipe::framed`-compatible, because the session codec escapes newlines inside strings. Choose one per far-end contract; never mix them.

## Any language

The session plugin `rutis-bridge/session` provides the service `rutis/sessions`:

```ts
type Close = { kind: 'done' } | { kind: 'replaced' } | { kind: 'rejected'; reason: string }
interface Far  { send(message: Uint8Array): void; close(why: Close): void }
interface Near { receive(message: Uint8Array): Promise<void>; end(why: Close): void }

open(far: Far): Near
```

```ts
export default definePlugin({
  name: 'relay',
  inject: ['rutis/sessions'],
  async apply(ctx, config) {
    const connection = await connect(config) // admission, before any session
    const near = await ctx.use('rutis/sessions').open({
      send: message => connection.send(message),
      close: why => connection.close(why),
    })
    connection.onMessage(message => near.receive(message))
    connection.onClose(() => near.end({ kind: 'done' }))
    ctx.effect(() => () => connection.close({ kind: 'done' }))
  },
})
```

- The service is `rutis/sessions`, not `sessions` (0.19.3 renamed it). Declare it in `inject`; without it, `open` fails with `declare rutis/sessions in inject`. The old name is not kept, and `sessions` is left to the host's own services.
- The session is mounted under the plugin that calls `open`: when that plugin unloads, its sessions and their remote plugins unload.
- `receive` completes when the message is in the session's receive queue; the queue is 64 messages, and a full queue waits, carrying backpressure to the connection.
- The far end's plugins here are called `<far id>:<plugin>`; to make the far end run a plugin, write a row `<connection row>:<plugin>` (for example `relay:contacts`). While the far end is absent, that row waits.
- There is no `peer`, `token`, `credential` or `identity` parameter. Do not add one.
- `open(far)` returns `Near` immediately and does not expose the far end's `Hello` id. Only Rust's `Session::handshake` gives `Greeted::id()` before mounting. A non-Rust listen bridge therefore cannot do same-id replacement as of 0.21.0. `session_key(row)` is first-wins: a second session under the same row registers no handle, so `<row>:<plugin>` only addresses the first. Do not write "coexist"; see "Listen-side replacement" below.

## Callbacks and 0.21

Since 0.21, a function handed over by reference is asynchronous by default: the session awaits `far.send`, so a bridge whose `send` returns a Promise applies transport backpressure instead of buffering. Inside a synchronous call it still returns the value itself. `near.receive` is likewise awaited; do not drop its Promise without handling it.

`far.close` is called once, when the session closes the channel. Keep it idempotent: the adapter may also close the connection from its own side.

## Close semantics

| Where | What to do |
| --- | --- |
| Far end closed normally | `near.end({ kind: 'done' })` |
| Far end replaced by a newer connection of the same id | `near.end({ kind: 'replaced' })` |
| Far end refused after the channel was established | `near.end({ kind: 'rejected', reason })` |
| The session unloads | close the adapter connection once |
| The link plugin unmounts | close the adapter connection; its sessions unload |

Rust `Channel` has `Close::Done`, `Close::Replaced`, `Close::Rejected(reason)`; map them to the transport's close code where it has one (WebSocket: 1000, 4002, 4003).

The session has **no half-close**: `near.end` ends everything. EOF in one direction ends the whole connection and closes both directions exactly once; do not wait for a FIN in the other direction.

## Lifecycle rules

- One channel = one session. A listen side opens one session per accepted connection.
- `Channel` closes → session unloads; session unloads → channel closes. Do not create a second lifecycle.
- Reconnect/backoff belongs to the link plugin. The session never reconnects.
- Heartbeat belongs to the adapter (SSH keepalive, WebSocket ping, gRPC keepalive, Tailscale's own); the bridge does not invent a generic heartbeat frame. A failed heartbeat closes the connection and maps to `near.end`.
- Since 0.20, unloading runs one reverse stack, one entry at a time: register the adapter cleanup once (in `apply`, `ctx.effect`, or the cleanup `apply` returns); a cleanup cannot register another cleanup or wait for another one.
- Since 0.20, a session sends on its own writer thread. `rutis/sessions.open(far)` leaves the outbound queue unbounded (`SessionOptions::outbound_limit` is a host option), and a bridge-side queue does **not** bound it. Rust sets `Session::options(SessionOptions { outbound_limit: Some(bytes) })`; non-Rust `open(far)` has no such option, so this is a blocking API gap (O2).
- `rutis-host/listen` uses the far end's `Hello` id for replacement: a newer connection with the same id unloads the old session's remote plugins, closes the old connection with 4002, then mounts the new session. This is Rust-only today; the session core keeps no ids and replaces nothing.
- A handshake failure on a listen side does not print; the dialer writes the reason.

## Listen-side replacement

Replacement needs the far end's `Hello` id **after** the handshake and **before** mounting.

- Rust: `Session::handshake` → `Greeted::id()` → unload the old session → `ctx.plugin(greeted)`.
- Other languages: `open(far)` returns `{ receive, end }`; there is no id. Parsing the session `Hello` in the bridge is forbidden (the bridge is transport, not a second session layer). Do not substitute the adapter's admission identity (SSH user, tailnet node, token): the contract's id is the far end's registration.

Until rutis exposes the id to `open` (O1), a non-Rust listener cannot replace. Do **not** fall back to "one session per connection, same id coexists": `session_key(row)` is first-wins, so the second session registers no handle and `<row>:<plugin>` only addresses the first. Implement the listener in Rust (`Session::handshake`).

## Errors

Match codes, not text: 0.21 changed almost every sentence. `error.code` and the keys of `error.data` are the contract.

| Code | Meaning for a link plugin |
| --- | --- |
| `not-declared` | `rutis/sessions` is missing from `inject` (`data.service`) |
| `bad-argument` / `bad-value` | `open(far)` got something other than `{ send, close }`; `bad-value` carries `data.reason` (`non-finite`, `cycle`, `too-deep`, `type`, `tag`, `limit`) |
| `bad-result` | a result did not decode (`data.service`, `data.method`, `data.expected`) |
| `unavailable` | a declared service is not leased in |
| `timeout` / `not-running` / `no-service` | test-tool failures, not link-layer errors |

## What not to put in the session

- token, certificate, SSH user, Tailscale identity, Cloudflare Access token;
- a `peer` / `expected_id` parameter;
- a transport type check or a vendor name;
- reconnect/backoff;
- framing/encoding of the transport;
- a parse of the session `Hello` or any other session frame.

If you need any of these in the session, the boundary is wrong: put it in the link plugin.
