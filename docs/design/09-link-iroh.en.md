# `link/iroh` Rust P2P / relay prototype: design

Status: draft. Date: 2026-11-08. Baseline: rutis 0.21.0.
Issue: [#9](https://github.com/arcships/rutis-network/issues/9). Depends on [#1](https://github.com/arcships/rutis-network/issues/1).

## 1. Position

Give users who cannot install a system VPN and do not want a Cloudflare/ngrok central ingress an embeddable P2P/relay transport. iroh is a Rust QUIC + NAT traversal + relay library; dial by public key, end-to-end encrypted, with FFI.

**Prototype first, no production promise**: NAT traversal is moving, direct connections fall back to relay, and DNS/certificate capabilities vary by version.

## 2. Connection shape

```text
Endpoint A ── QUIC (ALPN rutis/link/1) ── Endpoint B
               one bidirectional stream = one session
```

- `Endpoint` + `ALPN`; the dialer knows the peer's `NodeId`/ticket.
- One QUIC bidirectional stream per session; do not multiplex several sessions on one stream.
- Admission: verify the configured expected `NodeId` on dial; keep a NodeId allowlist on listen, and require an application token when there is no allowlist; both before `Session::new` / `open`. Do not pass the iroh `NodeId` to the session; never log the token.
- The far end registers its id in `Hello`; `NodeId` is not the session id.

## 3. Framing

- A QUIC stream is bytes: use #6's 4-byte big-endian length + payload, max 16 MiB.
- One stream = one bridge instance = one session; stream close → `near.end`.
- 0.21 async `send`: QUIC write blocking applies backpressure through async `send`; the bridge queue is bounded.
- No extra compression/encryption above iroh (QUIC already has TLS).

## 4. Direct and relay

| Shape | Description | Risk |
| --- | --- | --- |
| Direct P2P | Hole punching succeeds, end-to-end encrypted | Success rate varies by network |
| Relay fallback | Forwards through a relay when punching fails | Higher latency/lower throughput; relay sees traffic metadata (still encrypted) |
| Public relay | Default/testing | Depends on a third party's availability/quota |
| Self-hosted relay | Production control | Operations cost |

Logs must show direct vs relay; document relay privacy/cost.

## 5. Lifecycle and reconnect

- Stream/connection closes → `near.end`; session unloads → close stream/connection.
- Reconnect lives in link/iroh: exponential backoff (0.5 s → 30 s); auth/permission rejection retries every 30 s; relay unavailability backs off; the backoff resets after 60 s connected.
- 0.20 unload order: register endpoint/connection cleanup once; do not register or wait for another cleanup.
- 0.21 async `send`: write blocking applies backpressure.

## 6. Listen side

- Rust uses `Session::handshake` for same-id replacement; non-Rust `open(far)` does not expose the id, and `session_key(row)` is first-wins, so non-Rust listeners do not support same-id replacement and must be Rust (O1). Do not write "coexist".
- Non-Rust also cannot get structured handshake errors before O1 (handshake result API), so it cannot reliably tell an auth rejection from a protocol mismatch; do not treat an iroh connection error as a handshake result, fall back to transport backoff and record the limitation.
- Do not parse `Hello`; do not substitute `NodeId` for the id.

## 7. Maturity and dependencies

- iroh's NAT traversal/relay/DNS capabilities change by version; pin a version and record it in the prototype.
- Dependency size: QUIC/TLS/crypto; evaluate mobile FFI binary size.
- No production SLA; benchmark first (direct/relay latency, throughput, success rate).

## 8. Errors

| Case | Handling |
| --- | --- |
| Peer offline / NodeId does not resolve | Report the id and discovery/DNS error |
| Hole punching fails | Record it and fall back to relay if configured |
| Relay unavailable | Report the relay address; back off |
| Auth/authorization failure | Report a structured reason + endpoint public-key fingerprint; never the token; retry every 30 s |
| Stream timeout/reset | `near.end`; reconnect |
| Frame over limit | Close; report 16 MiB |

## 9. Tests

- Two local endpoints: one QUIC stream, boundaries/order/16 MiB/close/backpressure.
- Direct and relay each once; logs distinguish them.
- Disconnect/reconnect/backoff; session unload closes the stream.
- Admission: an unauthorized NodeId / missing or wrong token is rejected and creates no session; logs contain no token.
- No n0 production account/public relay; use a local/self-hosted test relay.
- Benchmarks: latency/throughput/success rate (direct vs relay).
- At least one manual e2e step.

## 10. Non-goals

- No iroh as a core dependency.
- No rutis token/identity mapping.
- No production promise.
- No relay server (use public/self-hosted test relays).
- No libp2p/WebRTC implementation; comparison notes only.

## 11. Open questions

- O1: Use an iroh ticket or `NodeId` as the dial address? Is extra application discovery needed?
- O2: Public relay or self-hosted relay as the default test path?
- O3: Is mobile FFI binary size/power acceptable?
- O4 (decided): one QUIC stream per session; QUIC multistream is a future extension, not a current option.

## Links

- Issue [#9](https://github.com/arcships/rutis-network/issues/9)
- [Contract design](01-link-plugin-contract.en.md)
- [TCP bridge design](06-link-tcp.en.md)
- [gRPC design](07-link-grpc.en.md)
