# Link plugin checklist

Baseline: rutis 0.21.0 (session protocol 4, `rutis.json` format 1, plugin API 3).

## Contract

- [ ] The plugin is a normal plugin; it declares `rutis/sessions` in `inject` (or uses `Session::new` in Rust).
- [ ] Admission happens before `open` / `Session::new`; a rejected connection creates no session.
- [ ] The session layer contains no token, certificate, SSH user, Tailscale identity, Cloudflare Access token, or `peer` parameter.
- [ ] The far end's `Hello` id names remote plugins `<id>:<plugin>`; the link plugin does not assign or rewrite it.
- [ ] One channel = one session; session lifecycle is bound to the channel.
- [ ] Reconnect/backoff lives in the link plugin, not in the session.
- [ ] A listen side opens one session per connection. Same-id replacement is implemented in Rust with `Session::handshake` + `Greeted::id()`; non-Rust is **unsupported** (0.21.0 `open(far)` has no id, and `session_key(row)` is first-wins).
- [ ] No cross-link `links` manager, global `connect` service, cross-link registry or central credential store; composition is row + session + service name.
- [ ] WebSocket is the core's `websocket::dial` / `listen`, not a new link plugin; plain HTTP is not a link; localhost TCP is the first reference adapter.

## Framing

- [ ] Bytes are framed into messages; 4-byte length prefix is the default, newline framing only for `pipe::framed`-compatible far ends; never mix.
- [ ] Message boundaries and order survive.
- [ ] Maximum message size is enforced before allocation; over-limit closes cleanly. The 16 MiB limit is bridge policy, not a `Channel` contract.
- [ ] Backpressure: a slow receiver does not grow memory without bound; an async `send` is awaited; Rust sets `SessionOptions::outbound_limit`, non-Rust notes the API gap.
- [ ] Close / EOF maps to `near.end`; the session has no half-close; the transport closes both directions exactly once.
- [ ] No unbounded buffering, no hidden retry that replays session messages.

## Lifecycle

- [ ] Far end closes → `near.end` (done/replaced/rejected).
- [ ] Session unloads → adapter connection closes.
- [ ] Link plugin unloads → its sessions unload and connections close.
- [ ] The adapter cleanup is registered once; it does not register another cleanup or wait for one (0.20 unload order).
- [ ] Listen replacement: old remote plugins unload, old channel closes (4002 where the transport has it), new session mounts. Rust only.
- [ ] Heartbeat is adapter-specific (SSH keepalive, WebSocket ping, gRPC keepalive, Tailscale); the bridge adds no generic heartbeat frame.
- [ ] Handshake failure does not disturb existing sessions.
- [ ] Errors name the row/connection and the adapter address; no secrets.
- [ ] Errors are matched by `code`, not text (0.21).

## Tests

- [ ] Fake `Far` / `Near`: boundaries, order, max size, close, backpressure.
- [ ] Remote `Hello` id → `<id>:<plugin>`.
- [ ] Listen: multiple connections; same-id replacement where supported.
- [ ] Admission failure before `open`; no session created.
- [ ] Async `far.send` returns a promise that is awaited; a slow send does not grow the bridge queue.
- [ ] Real adapter smoke test with a local/fake service, not a public account.
- [ ] At least one cross-language conformance run if the language SDK provides it.
- [ ] Docs example is copy-paste runnable.
- [ ] `cargo test`, language tests, and `node tools/check-md-links.mjs` (when available) pass.

## Docs

- [ ] This repository's `docs/guide/link-plugins.md` and `.en.md` have the adapter section.
- [ ] What to install/configure; admission method; what is not passed to rutis.
- [ ] Normal output and error output; use error codes where the text is not stable (0.21).
- [ ] Reconnect/backoff and listen replacement (or its documented limitation).
- [ ] Security boundary: public ingress, relay, DERP, token scope, mTLS, ACL.
- [ ] The adapter is not described as a rutis core dependency.
