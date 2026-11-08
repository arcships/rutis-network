# `link/grpc` cloud control plane / relay / distributed cloud: design

Status: draft. Date: 2026-11-08. Baseline: rutis 0.21.0.
Issue: [#7](https://github.com/arcships/rutis-network/issues/7). Depends on [#1](https://github.com/arcships/rutis-network/issues/1).

## 1. Position

gRPC is not NAT traversal or device interconnect. It is an application protocol used after reaching a cloud endpoint:

- a host dials a cloud control plane (registration, routing, quota, observability);
- host ↔ host through a cloud relay;
- host → cloud runtime;
- inside K8s/service mesh;
- multi-region relay/failover for distributed cloud.

Priority: **P1 only when the cloud business exists; otherwise P2.** It does not block #3–#6.

## 2. Protocol shape

```proto
syntax = "proto3";
package rutis.link.v1;

service Link {
  // One bidi stream = one session.
  rpc Tunnel(stream Frame) returns (stream Frame);
}

message Frame {
  bytes payload = 1;   // one session message; gRPC keeps boundaries
}
```

- One bidi stream per session; do not multiplex several sessions on one stream unless a `session_id` field and separate lifecycles are designed later.
- `payload` is a session message; gRPC already keeps boundaries, so no length prefix.
- The control plane/relay can use the same service with metadata (`authorization`, `x-rutis-region`) or a separate admin service; not first.
- Put the protocol version in metadata or the first frame; do not change `payload` semantics.

## 3. Admission

| Method | Rule |
| --- | --- |
| mTLS | Client certificate; verify CN/SAN; do not pass certificate identity to the session |
| OAuth/JWT | `authorization: Bearer`; verify issuer/audience/scope |
| API key | Metadata; adapter configuration only |
| Failure | `UNAUTHENTICATED`/`PERMISSION_DENIED`; create no session |

The session `Hello` still registers identity; the gRPC principal is not mapped to `<id>`.

## 4. Limits

- The message limit is not a rutis fact; it is the chosen gRPC implementation's configuration (many default to 4 MiB). Configure both sides to at least 16 MiB explicitly, or define a split strategy.
- HTTP/2 proxies/load balancers have timeouts, buffering and maximum stream durations; use keepalive so an idle timeout does not cut a live session.
- Serverless scale-to-zero does not suit long streams; be always-on or accept reconnects.
- gRPC-Web does not support bidi; browsers use WebSocket/WebTransport.
- Proxies may not support HTTP/2 trailers; test error propagation before relying on it.

## 5. Lifecycle and reconnect

- Stream closes → `near.end`; session unloads → close the stream.
- Reconnect lives in link/grpc: exponential backoff (0.5 s → 30 s); no retry on `UNAUTHENTICATED`; retry on `UNAVAILABLE`/GOAWAY.
- 0.20 unload order: register stream/client cleanup once; do not register or wait for another cleanup.
- 0.21 async `send`: gRPC write blocking applies backpressure through async `send`; the bridge queue is bounded.
- Server GOAWAY: unload the session and reconnect with backoff; do not silently drop messages.

## 6. Cloud shapes

### 6.1 Control plane

- Host registration: id, region, capabilities, current load;
- Routing: choose a relay or runtime by id/region/tenant;
- Quota/observability: connections, messages, bytes, error codes;
- Never carries session content.

### 6.2 Relay

- Pair two streams: A ↔ relay ↔ B; the relay forwards `payload` and does not parse the session protocol;
- Multi-tenant: the tenant id comes from admission, not from the session;
- Pairing failure: both ends get a clear error;
- The relay does not own identity; the two `Hello`s do.
- **Boundary**: the relay/control plane is a separate cloud service or application, not the local link layer's manager/registry; it does not take part in local link composition and does not hold link credentials. This keeps #1 D11 intact.

### 6.3 Cloud runtime

- A gRPC stream to a rutis runtime in the cloud; semantics match local `rutis-host/spawn`;
- The runtime greeting: the session handshake is 10 s; the 30 s `Process::mount` / `rutis-host/spawn` limit only applies when this end starts a runtime process.
- Plugins run in the cloud; the session still names them `<id>:<plugin>`.

### 6.4 Multi-region

- Nearest region, session affinity, failover, data residency;
- On failover the old stream closes and a new stream handshakes; the session does not migrate state;
- Document what is lost (in-memory session/plugin state).

## 7. Listen side

- Rust uses `Session::handshake` to get `Greeted::id()` for same-id replacement; a relay that needs replacement uses Rust.
- Non-Rust `open(far)` does not expose the id, and `session_key(row)` is first-wins; multiple streams cannot share one row. A relay either uses Rust or gives each stream its own row/session.
- Do not parse `Hello`; do not substitute the gRPC principal for the id.

## 8. Errors

| Case | Handling |
| --- | --- |
| Unauthenticated/permission denied | `UNAUTHENTICATED`/`PERMISSION_DENIED`; do not `open` |
| Message over 4 MiB | Raise the limit or split; report clearly |
| Proxy timeout/GOAWAY | `near.end`; reconnect with backoff |
| Relay pairing failure | Clear error on both ends; do not hang |
| Cloud runtime greeting timeout | 10 s session handshake timeout; the 30 s `Process::mount` limit only applies to local spawn |
| Protocol version mismatch | Report the version; do not retry |

Errors use gRPC status plus structured details; rutis 0.21 error texts are not a contract.

## 9. Tests

- Local gRPC test service: dial, bidi stream, `open(far)`, remote plugin `<id>:<plugin>`.
- mTLS or OAuth failure: rejected; the session does not see it.
- 16 MiB: works after raising the limit, or the split strategy is verified.
- Stream disconnect: session unloads; reconnect; relay pairs again.
- GOAWAY/timeout: backoff; no silent loss (in-flight messages are acknowledged or reported).
- No real cloud account; CI uses a local service.

## 10. Non-goals

- No NAT traversal/device interconnect.
- No gRPC identity as session identity.
- No full multi-tenant cloud product first; protocol adapter + test service first.
- No gRPC-Web as a browser bidi solution.
- No multiplexing several sessions on one stream (for now).

## 11. Open questions

- O1: Does the proto live in rutis-network or the rutis repository? What version policy?
- O2 (closed): the relay does not parse `payload`; route by metadata/tenant only.
- O3: Is one stream per session (one HTTP/2 stream each) acceptable?
- O4: Should failover migrate session state? Not first.

## Links

- Issue [#7](https://github.com/arcships/rutis-network/issues/7)
- [Contract design](01-link-plugin-contract.en.md)
- [TCP bridge design](06-link-tcp.en.md)
- rutis `docs/guide/nodes.md`
