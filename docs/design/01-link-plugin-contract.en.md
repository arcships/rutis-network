# Link plugin contract: design

Status: draft. Date: 2026-11-08. Baseline: rutis 0.21.0 (2026-11-05), session protocol 4, `rutis.json` format 1, plugin API 3.
Issue: [#1](https://github.com/arcships/rutis-network/issues/1).
Companions: [SKILL.md](../../.agents/skills/rutis-link-plugin/SKILL.md), [link-plugin-api.md](../../.agents/skills/rutis-link-plugin/references/link-plugin-api.md).

## 1. Problem

0.19 moved the link layer out of the session core. The extension point is now an ordinary plugin that gets a bidirectional message stream and hands it to `rutis/sessions`. The docs cover WebSocket, pipe and memory, but not:

- which work belongs to the link layer and which to the session;
- how admission and identity are separated;
- how bytes become messages;
- who owns reconnect, close and listen replacement;
- how the 0.19.3 / 0.20 / 0.21 changes land in a bridge.

Without a contract every language decides these again, and authentication, identity and reconnect creep back into the session.

## 2. Decisions

### D1 The link layer is an ordinary plugin, not a core trait

- Any language; it uses `rutis/sessions` (Rust: `Session::new`).
- No `Transport`, `LinkPlugin` or `ChannelInfo`; no vendor in the core.
- The `Identity` / `Admission` types removed in 0.19 stay out: admission belongs to the link layer.

### D2 Responsibilities

| Layer | Owns | Does not own |
| --- | --- | --- |
| Link layer (adapter + bridge) | Reachability, admission, framing, heartbeat, reconnect, listen replacement, backpressure | The far end's identity, the session protocol, plugin naming |
| Session (`rutis/sessions` / `rutis-bridge/session`) | Handshake, mounting far plugins, services/events, lifecycle | Authentication, tokens, TLS, ACLs, transport details |
| Far end | Its id in `Hello`; its own plugins | How this end reaches it |
| Application plugin | Screen/input, business services | Transport and admission |

### D3 Adapter + bridge

```text
link plugin
  ├─ adapter: SSH / Tailscale / Cloudflare / gRPC / ADB / WebRTC / iroh / frp
  │    reachability + admission, gives a byte or message stream
  └─ bridge: framing + rutis/sessions.open(far) + lifecycle
```

- An adapter may be a CLI (`ssh`, `cloudflared`, `adb`, `iproxy`), an SDK or a sidecar.
- The bridge is written once per language; adapters do not repeat framing, close or backpressure.
- One channel = one session; several connections = several sessions.

### D4 Admission is not identity

- Admission happens **before** `open` / `Session::new`. On failure, close and create no session.
- Identity comes from the session `Hello` (`endpoint`); rutis trusts it and names remote plugins `<id>:<plugin>`.
- `open(far)` gets no `peer`, `token`, `credential` or `identity`.
- An SSH user, tailnet node or Access token is not passed to the session and is not a substitute for the `Hello` id.

### D5 Normalize the byte stream

- Prefer a length prefix: a 4-byte big-endian length followed by exactly that many bytes; reject an over-limit length before allocating.
- Maximum message: 16 MiB, the WebSocket default; close cleanly when exceeded.
- Preserve order and backpressure; the bridge's send queue is bounded.
- Map close / half-close to `near.end({kind:'done'})`; close the transport once.
- Do not parse or rewrite the session protocol; the bridge is transport, not a second session layer.

### D6 Lifecycle

- `Channel` closes → session unloads; session unloads → close the channel.
- Reconnect/backoff lives in the link layer; the session never reconnects.
- Since 0.20, unloading is one reverse stack: register the adapter cleanup once; a cleanup cannot register or wait for another cleanup.
- Since 0.20, a session sends on its own writer thread and `open(far)` leaves the outbound backlog unbounded; the bridge must bound its own queue.
- Since 0.21, received functions are asynchronous by default: `send` may be async, the session awaits it, and transport write blocking applies backpressure.

### D7 Listen replacement

- Rust: `Session::handshake(&ctx, channel).await` → `Greeted::id()`; unload the old session of that id, then `ctx.plugin(greeted)`, closing the old channel with `Close::Replaced` (4002).
- Other languages: in 0.21.0 `open(far)` returns only `{ receive, end }` and does not expose the far id. Same-id replacement is therefore **not possible**; open one session per connection and document that two connections from the same far id coexist.
- Do not parse `Hello` in the bridge and do not substitute the adapter's admission identity.

### D8 Errors

- 0.21 changed almost every error text; match `code` and the keys of `data`.
- `bad-value` has `data.reason`: `non-finite`, `cycle`, `too-deep`, `type`, `tag`, `limit`.
- `bad-result` has `data.service`, `data.method`, `data.expected`.
- Link-layer errors name the row/connection and the adapter address; no secrets.

### D9 Reference implementation and conformance contract

- Ship one reference bridge (Rust or TS); language templates belong to #10.
- Conformance contract: boundaries, order, 16 MiB limit, async send, backpressure, close/half-close, `<id>:<plugin>` naming, admission failure before `open`, unload closes the connection, Rust listen replacement.

### D10 localhost TCP / WebSocket are first; HTTP is not a link

- The core already has `websocket::dial` / `websocket::listen`, and `rutis-host/listen` uses it; do not add `link/websocket`.
- The reference bridge starts with localhost TCP (#6) or the existing WebSocket; both bind `127.0.0.1`, and loopback still needs a random port or one-time token.
- Plain HTTP request/response has no duplex long-lived stream or message boundaries; HTTP only carries a WebSocket upgrade or gRPC.

### D11 Composition is row + session + service name; no cross-link center

- A link plugin exposes exactly two things: one configuration row in `rutis.json`, and the session it mounts.
- Business plugins do not know link plugins. A `<link row>:<plugin>` row makes the far end run a plugin; once its service is up, business plugins `inject` / `use` it by service name.
- No global `links` manager, global `connect` service, cross-link registry, or central credential store. Each row owns its admission, credentials, retries, listeners and child processes.
- Dynamic changes go through `rutis/loader` / `rutis.json`; that is deployment/operations, not a link-layer runtime API. Prefer always-on rows with reconnect.
- Each link may have an optional `status` / `doctor` / CLI as its own external tool; do not build a cross-link central manager.

```text
business plugin ── use ──> far service
                             ▲
                        session (rutis/sessions)
                             ▲
link row (link/ssh etc.) ── transport/admission/framing/reconnect ──> far end
```

## 3. Interface

Rust:

```rust
let channel = adapter.connect(&config).await?;   // admission already done
let session = ctx.plugin(Session::new(channel));  // session lifecycle == channel lifecycle
```

TypeScript:

```ts
const near = ctx.use('rutis/sessions').open({
  send: message => connection.send(message), // 0.21: may be async
  close: why => connection.close(why),
})
connection.onMessage(message => near.receive(message))
connection.onClose(() => near.end({ kind: 'done' }))
```

## 4. Tests

- Fake `Far` / `Near`: boundaries, order, maximum message, close, backpressure.
- Async `send` returns a promise and is awaited; a slow send does not grow the bridge queue.
- Far `Hello` id → `<id>:<plugin>`.
- Listen: Rust multiple connections plus same-id replacement; other languages allow coexistence.
- Admission fails before `open`; no session is created.
- Session unload closes the adapter connection.
- No token or certificate in output.

## 5. Non-goals

- No new core trait or API.
- No peer/token/credential on `open`.
- No authentication in the session.
- No Rust-only link layer.
- No cross-link `links` manager, global `connect` service or central credential store; composition is row + session + service name.
- No templates, catalogue or CI here (#10).

## 6. Open questions

- **O1 (blocks non-Rust listen replacement)**: rutis needs to expose the far id after the handshake through `rutis/sessions.open` (for example `near.id` / `ready()`). Until then, non-Rust listeners do not replace. This is a separate rutis API discussion; parsing `Hello` is not a workaround.
- **O2**: 4-byte or 8-byte frame length? Design #6 chooses 4 bytes; 16 MiB is enough, and a larger frame can extend it later.
- **O3**: 0.21 async callbacks run synchronously on a 0.20 runtime; a bridge must not assume `send` is always async.

## 7. Version impact

| Version | Impact |
| --- | --- |
| 0.19.3 | Service `sessions` → `rutis/sessions`; the old name is not kept |
| 0.20 | One reverse unload stack; session writer thread; `outbound_limit` unbounded by default |
| 0.21 | Received functions asynchronous by default; error texts changed, match codes |

## Links

- Issue [#1](https://github.com/arcships/rutis-network/issues/1)
- [SKILL.md](../../.agents/skills/rutis-link-plugin/SKILL.md)
- [link-plugin-api.md](../../.agents/skills/rutis-link-plugin/references/link-plugin-api.md)
- rutis `docs/guide/plugin-api.md#your-own-connection-rutissessionsopen`
- rutis `docs/design-remote-session-2026-10-28.md` §4, §5
- rutis `crates/rutis-bridge/src/session/plugin/open.rs`
