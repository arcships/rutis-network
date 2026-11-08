# `link/tailscale`: design

Status: draft. Date: 2026-11-08. Baseline: rutis 0.21.0.
Issue: [#4](https://github.com/arcships/rutis-network/issues/4). Depends on [#1](https://github.com/arcships/rutis-network/issues/1), [#6](https://github.com/arcships/rutis-network/issues/6).

## 1. Goal

Give individuals and small teams a cross-NAT path without public ports, certificates or DNS work. Tailscale is an adapter, not a rutis underlay: the link plugin dials tailnet TCP, the bridge frames, and the stream goes to `rutis/sessions`.

## 2. Shapes

### 2.1 system (first)

- Use the system `tailscaled`; dial/listen tailnet TCP (MagicDNS name or `100.x`).
- The user installs Tailscale and logs in; the link plugin only uses the local tailnet.
- Pro: no Go dependency; `tailscale status` shows direct/relay.
- Con: needs a system daemon; containers/no-root are awkward.

### 2.2 tsnet (Go, optional)

- Use `tailscale.com/tsnet` to join the tailnet in-process; `Listen`/`Dial` give TCP.
- Pro: no system daemon; good for embedded/containers.
- Con: Go dependency size; control-plane/state directory configuration; other languages need a wrapper.

### 2.3 sidecar (other languages)

- Run a `tsnet` sidecar locally; the link plugin connects to loopback TCP given by configuration.
- Pro: any language; the sidecar lifecycle can follow the link plugin.
- Con: one more process; loopback admission must be restricted (127.0.0.1 only, random port or one-time token).

## 3. Admission

| Item | Rule |
| --- | --- |
| Network | tailnet ACL / device identity; no rutis token |
| Device | Tailscale login/node authorization; a failure is reported in the link layer |
| Identity | Still registered by the session `Hello`; do not map a tailnet node/user to session identity |
| Sidecar | Bind 127.0.0.1 only; random port or one-time token, so local processes cannot wander in |

## 4. Connection and framing

- One tailnet TCP connection = one session; use #6's length-prefixed framing (4-byte big-endian length + payload, max 16 MiB).
- Do not run the existing WebSocket over Tailscale and call it link/tailscale; the adapter only reaches, the bridge keeps message boundaries.
- 0.21 async `send`: TCP write blocking applies backpressure through async `send`; the bridge queue is bounded.

## 5. Lifecycle and reconnect

- tailnet TCP closes → `near.end`; session unloads → close the connection.
- Reconnect lives in the link plugin: exponential backoff (0.5 s → 30 s); do not retry forever on ACL denial/certificate expiry.
- 0.20 unload order: register connection/daemon cleanup once; do not register or wait for another cleanup.
- direct vs DERP: `tailscale status` distinguishes them; DERP fallback changes throughput/latency; the log must show the current path.

## 6. Listen side

- system mode can use Rust's `Session::handshake` for same-id replacement; non-Rust `open(far)` does not expose the id, and `session_key(row)` is first-wins, so non-Rust listeners do not support same-id replacement and must be Rust (O1). Do not write "coexist".
- tsnet/sidecar listeners accept in the adapter; still one connection = one session.
- Do not parse `Hello`; do not substitute the tailnet node name for the id.

## 7. Errors

| Case | Handling |
| --- | --- |
| Tailscale not running/logged in | Report `tailscale status` and what to do |
| MagicDNS does not resolve | Report the name and DNS error; suggest `100.x` |
| ACL denial | Report it; do not treat it as a timeout and retry |
| DERP fallback | Connect but record the path; document the performance risk |
| Certificate/key expiry | Report expiry; ask for a new login |
| Sidecar not running | Report the sidecar address and how to start it |

## 8. Security boundary

- The tailnet ACL is the admission boundary; rutis trusts what the tailnet lets in.
- Do not pass tailnet identity to the session; do not log auth keys.
- The sidecar binds loopback only, so the port is not exposed to the LAN/tailnet.
- Tailscale Funnel/Serve is ingress/deployment, not a link plugin.

## 9. Tests

- Unit: fake TCP + length-prefix framing; boundaries/order/16 MiB/close/backpressure.
- system mode: two tailnet machines, one dials and one listens, `open` works.
- tsnet or sidecar at least once, proving no system daemon dependency.
- ACL denial: connection fails with a clear error.
- Reconnect: backoff, no endless retry on auth failure.
- CI does not depend on a real tailnet; manual e2e steps live in the docs.

## 10. Non-goals

- No Tailscale as a core dependency.
- No requirement for every rutis user to install Tailscale.
- No Serve/Funnel wrapper.
- No Headscale control plane; compatibility notes only.
- No tailnet identity as session identity.

## 11. Open questions

- O1: Should the sidecar expose a fixed protocol or plain local TCP? A fixed protocol can restrict it to tailnet forwarding.
- O2: Is the Go tsnet dependency worth it for "no daemon", or is the sidecar enough?
- O3: Should Headscale compatibility run in CI (local container)?

## Links

- Issue [#4](https://github.com/arcships/rutis-network/issues/4)
- [Contract design](01-link-plugin-contract.en.md)
- [TCP bridge design](06-link-tcp.en.md)
- [Cloudflare design](05-link-cloudflared.en.md)
- rutis `docs/guide/nodes.md`
