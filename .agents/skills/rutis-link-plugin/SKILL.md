---
name: rutis-link-plugin
description: Use when extending the rutis link layer — adding a connection plugin or adapter for SSH, Tailscale, Cloudflare Tunnel, gRPC, ADB/usbmuxd, iroh/libp2p/WebRTC, frp/rathole, a cloud relay, or any other way of reaching a far end. Covers the link-layer contract, adapter vs bridge, admission vs identity, framing, `rutis/sessions.open(far)`, lifecycle, tests, and the anti-patterns that would couple authentication or identity into the session layer. Any language; the core does not change.
---

# Adding a link plugin to rutis

rutis (https://github.com/arcships/rutis) is a plugin host with a Rust core and plugins in several languages. A **link plugin** (also called a connection plugin) is how a rutis host reaches a far end: a language runtime, another rutis, a device app, a cloud service.

This skill is written against **rutis 0.21.0** (2026-11-05): session protocol 4, `rutis.json` format 1, plugin API 3. When the host is older, check the version notes below; the extension point itself has not changed since 0.19.

Reference material is in `references/`:

| File | Contents |
| --- | --- |
| `references/link-plugin-api.md` | `rutis/sessions.open(far)` / `Session::new(channel)`, `Far` / `Near`, close, lifecycle |
| `references/scenarios.md` | Which adapters fit which user scenario; candidate landscape and priority |
| `references/checklist.md` | Test matrix and review checklist |

Keep one sentence in mind: **the link layer owns admission, reachability, framing and lifecycle; it does not own identity.** The far end registers its id in the session `Hello`; rutis trusts that registration. The session layer has no credentials and no authentication.

## 0. Version notes that change the contract

| Version | Change | What a link plugin does |
| --- | --- | --- |
| 0.19.3 | `sessions` / `loader` renamed to `rutis/sessions` / `rutis/loader`; the old names are not kept | `inject: ['rutis/sessions']`, `ctx.use('rutis/sessions')`; the failure is `declare rutis/sessions in inject` |
| 0.20.0 | Unloading runs one reverse stack, one entry at a time; a cleanup cannot register another cleanup or wait for one | Register the adapter close once in `apply` / `ctx.effect`; do the whole cleanup in that one entry |
| 0.20.0 | A session sends on its own writer thread; `SessionOptions::outbound_limit` is a host option, unbounded by default; `rutis/sessions.open(far)` uses the default | Bound the link plugin's own send queue; do not rely on the session to apply a limit. `far.send` may block its session's writer, not the process |
| 0.21.0 | A received function is asynchronous by default (Promise in TS/Bun, awaitable in Python) | `far.send` / `far.close` may return a promise; the session awaits `send`, so async transport backpressure works. `near.receive` is awaited |
| 0.21.0 | Error texts changed; codes and `data` keys are the contract | Match `error.code`, not the sentence. `bad-value` carries `data.reason` (`non-finite`, `cycle`, `too-deep`, `type`, `tag`, `limit`); a result that does not decode is `bad-result` |

## 1. Decide whether it is a link plugin

A link plugin produces a **message channel** to a far end. It is not:

- an application plugin that happens to use the network (a remote-control plugin, a screen/input service, an HTTP client);
- a session-layer feature (remote plugin naming, services, events, lifecycle);
- a core trait. 0.19 removed `Transport`, `LinkPlugin` and `ChannelInfo`; do not bring them back. The extension point is a normal plugin calling `rutis/sessions.open(far)` (or Rust `Session::new(channel)`).

Ask: **does this thing give a bidirectional, reliable, ordered message stream (or a byte stream I can frame), and is it responsible for admitting the connection?** If yes, it can be a link plugin. If it gives IP packets, put TCP (and framing) inside the plugin. If it is a remote-control protocol (RDP/VNC/SPICE/RustDesk), it is usually an application plugin, not a link plugin.

## 2. Choose the adapter shape

A link plugin has two parts; keep them separate:

```text
link plugin (any language)
  ├─ adapter: SSH / Tailscale / Cloudflare / gRPC / ADB / WebRTC / frp / iroh / …
  │    reachability + admission, gives a stream
  └─ bridge: framing + rutis/sessions.open(far) + lifecycle
```

| Adapter shape | Examples | What the adapter gives | What the bridge does |
| --- | --- | --- | --- |
| Direct stream | TCP, Unix socket, pipe, WebSocket, QUIC, WebTransport | bytes or messages | frame if bytes; `open(far)` |
| Overlay network | Tailscale/Headscale, ZeroTier, Nebula, NetBird, WireGuard, EasyTier, OpenZiti | TCP over the overlay | frame; `open(far)` |
| Reverse tunnel | SSH `-L`/`-R`, Cloudflare Tunnel, frp, rathole, ngrok, bore, chisel | local or public TCP | frame; `open(far)` |
| P2P library | iroh, libp2p, WebRTC data channel | QUIC stream / data channel | frame if bytes; `open(far)` |
| Cloud link | gRPC bidi stream, cloud relay | messages | `open(far)` per stream |
| Mobile device | ADB `forward`/`reverse`, usbmuxd/`iproxy` | local TCP | frame; `open(far)` |

Any language works. Pick the cheapest implementation that fits the target language:

- shell out to the vendor CLI and use stdio or a loopback TCP port;
- use the vendor SDK/library;
- run a small sidecar (for example a Go `tsnet` sidecar) and connect to it over loopback.

The bridge is shared; write it once per language. Do not reimplement framing and lifecycle in every adapter.

## 3. Admission is the adapter's job

Decide how the adapter admits a connection **before** any session exists:

| Adapter | Typical admission |
| --- | --- |
| SSH | host key + user key / agent / `authorized_keys` |
| Tailscale | tailnet ACL / device identity |
| Cloudflare Tunnel | Cloudflare Access service token / WARP identity |
| gRPC | mTLS, OAuth/JWT, API key |
| ADB | ADB key + device authorization |
| usbmuxd | iOS trust / pairing |
| frp/rathole/ngrok | tunnel token / TLS / provider auth |
| iroh/libp2p/WebRTC | endpoint key / DTLS identity / app token |

If admission fails, close the connection and do not call `open`. Never pass a token, certificate, Tailscale identity or SSH user into the session layer.

**Identity is not admission.** After the connection is admitted, the far end says who it is in the session `Hello` (`endpoint`). rutis uses that id to name remote plugins (`<id>:<plugin>`) and trusts it. Do not add a `peer` parameter to `open`, and do not try to pass the adapter's identity into session naming.

## 4. Normalize the stream to messages

`Channel` / `Near` carries messages with boundaries. If the adapter gives bytes, frame them in the bridge:

- length-prefixed framing is preferred; newline framing is acceptable when the payload cannot contain a raw newline (the session JSON codec escapes them);
- set and enforce a maximum message size (the WebSocket transport defaults to 16 MiB);
- preserve order and backpressure; do not buffer without bound;
- map close / half-close to `near.end` and close the far end exactly once;
- do not parse or rewrite the session protocol; the bridge is transport, not a second session layer.

A length-prefixed frame is a 4-byte or 8-byte big-endian length followed by exactly that many bytes. Reject a length over the limit before allocating it. Do not use a self-describing format: the session already carries JSON values inside the message.

## 5. Mount the session and bind its lifecycle to the channel

Rust:

```rust
let channel = adapter.connect(&config).await?;
let session = ctx.plugin(Session::new(channel));
// The session ends when the channel ends; reconnect is this plugin's decision.
```

Other languages:

```ts
export default definePlugin({
  name: 'link-ssh',
  inject: ['rutis/sessions'],
  async apply(ctx, config) {
    const connection = await adapter.connect(config) // admission is here
    const near = ctx.use('rutis/sessions').open({
      send: message => connection.send(message), // may be async in 0.21
      close: why => connection.close(why),
    })
    connection.onMessage(message => near.receive(message))
    connection.onClose(() => near.end({ kind: 'done' }))
    ctx.effect(() => () => connection.close({ kind: 'done' }))
  },
})
```

- One channel = one session. If the far end has several connections, open one session per connection.
- `Channel` closes → session unloads; session unloads → close the channel. Do not create a second lifecycle for identity or authentication.
- Reconnect/backoff lives in the link plugin, not in the session. The session never reconnects.
- Register the adapter cleanup once, in `apply` / `ctx.effect`. Since 0.20, a cleanup cannot register or wait for another cleanup.

**Listen-side replacement.** In Rust, `Session::handshake(&ctx, channel).await` gives `Greeted::id()` before mounting; this is how `rutis-host/listen` unloads an older session of the same far id first, closes it with `Close::Replaced` (4002), then mounts the new one. In other languages, `rutis/sessions.open(far)` returns only `{ receive, end }` as of 0.21.0 and does **not** expose the far end's id, so a non-Rust listen bridge cannot implement same-id replacement without parsing the session protocol (forbidden). Until rutis exposes the id, either:

- write the listener in Rust with `Session::handshake`, or
- accept one session per connection and document that two connections from the same far id coexist.

Do not guess the id from the adapter (SSH user, tailnet node, token): that is admission identity, not the far end's `Hello` id.

## 6. Test the contract, not the vendor

Use a fake `Far` / `Near` or a local echo for the bridge:

- [ ] message boundaries and order survive;
- [ ] maximum message is enforced; over-limit closes cleanly;
- [ ] backpressure: a slow receiver does not grow memory without bound; an async `send` is awaited;
- [ ] close: far end closes → `near.end`; near end ends → far end closes;
- [ ] remote `Hello` id → `<id>:<plugin>` naming;
- [ ] listen: multiple connections; same-id replacement (Rust, or conditional on rutis exposing the id);
- [ ] admission failure happens before `open`; no session is created;
- [ ] session unload does not leave the adapter connection open;
- [ ] no token/credential appears in session code or output.

Then test the real adapter separately (SSH/Tailscale/Cloudflare/etc.) with a local or fake service; do not make CI depend on a public account.

## 7. Document what the user sees

Write a short section in `docs/guide/link-plugins.md` (and the `.en.md` counterpart):

- what the adapter needs installed/configured (SSH server, Tailscale, `cloudflared`, ADB, relay);
- admission method and what is *not* passed to rutis;
- how to configure the plugin (address, mode, credentials source);
- normal output and error output, using codes where the text is not stable (0.21 changed almost every sentence);
- reconnect/backoff and listen replacement;
- security boundary (public ingress? relay? DERP? token scope?).

## Anti-patterns

- ❌ Adding `peer`, `token`, `credential` or `identity` to `rutis/sessions.open`.
- ❌ Making the session layer authenticate, or know about Tailscale/SSH/Cloudflare.
- ❌ Reintroducing `Transport`, `ChannelInfo.peer`, `Identity` / `Admission` in the core.
- ❌ Making the link plugin Rust-only; it is a normal plugin, any language.
- ❌ Treating RDP/VNC/SPICE as a generic link; they are application protocols. Use RDP DVC / WebRTC data channel / ADB only as the specific exception.
- ❌ Putting reconnect, listen replacement or transport errors into the session.
- ❌ Hard-coding a vendor as a rutis dependency; keep it an adapter.
- ❌ Matching on error text (0.21 changed it); match `error.code` and the keys of `error.data`.
- ❌ Registering a cleanup from a cleanup, or waiting for another cleanup (0.20 unload order).

## References

- `references/link-plugin-api.md` — `rutis/sessions.open`, `Session::new`, `Far`/`Near`, close.
- `references/scenarios.md` — scenario → adapter choice; candidate landscape; T1/T2/T3.
- `references/checklist.md` — test and review checklist.
- rutis repository (0.21.0): `docs/guide/plugin-api.md` (your own connection), `docs/guide/nodes.md`, `docs/design-remote-session-2026-10-28.md`, `docs/design-protocol-channel-decoupling-2026-10-03.md`, `crates/rutis-bridge/src/channel/mod.rs`, `crates/rutis-bridge/src/session/plugin/mod.rs`, `crates/rutis-bridge/src/session/plugin/open.rs`.
