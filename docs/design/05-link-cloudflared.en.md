# `link/cloudflared`: design

Status: draft. Date: 2026-11-08. Baseline: rutis 0.21.0.
Issue: [#5](https://github.com/arcships/rutis-network/issues/5). Depends on [#1](https://github.com/arcships/rutis-network/issues/1), [#6](https://github.com/arcships/rutis-network/issues/6).

## 1. Goal

Cover the case where the dialer cannot install a VPN and the listener is behind NAT with no public port: the listener runs Cloudflare Tunnel outbound, and the dialer reaches it over WSS (WebSocket upgrade) or `cloudflared access tcp`.

Cloudflare only reaches (and optionally admits via Access); the link plugin frames and calls `rutis/sessions.open(far)`; rutis does not know Cloudflare.

## 2. Shapes

### 2.1 access (first)

```text
dialer link/tcp ── 127.0.0.1:<local> ── cloudflared access tcp ── Cloudflare ── listener
```

- The dialer runs `cloudflared access tcp --hostname <host> --url 127.0.0.1:<port>`.
- The link plugin connects to loopback TCP and uses #6's `link/tcp` bridge.
- Cloudflare Access service token/identity admits; the Access identity is not passed to the session.
- Pro: the dialer only needs `cloudflared`; no public listening port.
- Con: one more process; the Access token must be managed.

### 2.2 public (optional)

- The listener runs `cloudflared tunnel` and exposes local rutis WSS/TCP at `wss://<host>`.
- The link plugin connects to `wss://<host>` (WebSocket upgrade) or Cloudflare's TCP transport; plain HTTPS request/response is not a link.
- Cloudflare Access or a link-layer token admits; document the public-ingress risk.
- "Public ingress without Access" is not the default recommendation.

### 2.3 WARP

- WARP is device-side network access, not a link-plugin adapter; document how it differs from access/public.
- Do not pass WARP identity to the session.

## 3. Admission

| Item | Rule |
| --- | --- |
| Access | Service token/identity; the token lives only in adapter configuration |
| public | Access or a link-layer token; Access is the default |
| Identity | Still registered by the session `Hello`; do not map Access email/service token to `<id>` |
| Failure | Close; do not call `open`; create no session |

## 4. Connection and framing

- access mode gives local TCP: use #6's 4-byte big-endian length + payload, max 16 MiB.
- public mode: WebSocket messages already have boundaries, so call `open` directly; if TCP, frame.
- 0.21 async `send`: WebSocket/`cloudflared` write blocking applies backpressure through async `send`; the bridge queue is bounded.
- Long connections: Cloudflare's HTTP/2/WebSocket proxy has timeouts and buffering that vary by plan; document and test reconnect.

## 5. Lifecycle and reconnect

- `cloudflared` exits / WebSocket closes → `near.end`; session unloads → close the connection/process.
- Reconnect lives in the link plugin: exponential backoff (0.5 s → 30 s); an Access rejection retries every 30 s; a config/protocol error stops; the backoff resets after 60 s connected.
- 0.20 unload order: register the `cloudflared` child kill/wait once; do not register or wait for another cleanup.
- Child stderr belongs in link-plugin errors, not in the session byte stream.

## 6. Listen side

- The listener usually sits behind `cloudflared tunnel`; Rust uses `Session::handshake` for same-id replacement.
- Non-Rust `open(far)` does not expose the id, and `session_key(row)` is first-wins, so non-Rust listeners do not support same-id replacement and must be Rust (O1). Do not write "coexist".
- Non-Rust also cannot get structured handshake errors before O1 (handshake result API), so it cannot reliably tell an auth rejection from a protocol mismatch; do not treat a `cloudflared`/WebSocket close reason as a handshake result, fall back to transport backoff and record the limitation.
- Do not parse `Hello`; do not substitute the Access identity for the id.

## 7. Errors

| Case | Handling |
| --- | --- |
| `cloudflared` missing/not running | Report the binary and start command |
| Access denial | Report 403/Access error; retry every 30 s, no busy loop |
| Tunnel disconnect | `near.end`; reconnect with backoff |
| WebSocket/HTTP2 proxy timeout | Report timeout; mention long-connection limits |
| Public ingress misconfiguration | Report hostname/route; never print the token |
| Frame over limit | Close; report 16 MiB |

## 8. Security boundary

- Public ingress is Internet-reachable; enable Access by default and do not expose rutis naked.
- The Access token lives in adapter config/environment; not in the session, not in logs.
- Do not build `cloudflared` arguments from session messages.
- Document what Cloudflare sees (TLS termination, Access logs).

## 9. Tests

- Local fake tunnel / local `cloudflared access`: access + `link/tcp` runs a session.
- public mode: a local WSS test service works; Access denial is clear.
- Reconnect/backoff; session unload closes the connection.
- CI does not depend on a Cloudflare account; manual e2e steps live in the docs.

## 10. Non-goals

- No Cloudflare Tunnel server.
- No Cloudflare as a core dependency.
- No Access identity as session identity.
- No Workers/Pages integration.
- Do not make HTTP reverse proxying the only WebSocket link; access TCP is more general.

## 11. Open questions

- O1: Access service token via environment or `cloudflared` config? How to keep it out of logs.
- O2: Should public mode require Access, or allow a link-layer token? How do they combine?
- O3: Pin long-connection timeout values, or document "by plan/measurement"?

## Links

- Issue [#5](https://github.com/arcships/rutis-network/issues/5)
- [Contract design](01-link-plugin-contract.en.md)
- [TCP bridge design](06-link-tcp.en.md)
- [Tailscale design](04-link-tailscale.en.md)
- rutis `docs/guide/nodes.md`
