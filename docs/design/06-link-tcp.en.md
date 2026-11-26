# TCP byte-stream bridge and reverse tunnels

Finalized on 2026-11-26 against rutis 0.23.0 (session protocol 5, plugin API 3). [Issue #6](https://github.com/arcships/rutis-network/issues/6) tracks scope and acceptance. [中文](06-link-tcp.md)

## Boundary and framing

SSH forwarding, frp, Tailscale, Cloudflare Access, ADB and usbmuxd can all provide a byte stream. The adapter reaches and admits the peer before handing each verified stream to Go’s `tcp.Open`. The Go bridge implements #11’s wire contract and interoperates with its TypeScript reference. The bridge owns framing, backpressure, closure and one `sessions.open(far)` per stream. rutis owns Hello, identity and service lifecycle. The existing WebSocket transport keeps its native message boundaries. [Link contract](01-link-plugin-contract.en.md)

A frame contains an unsigned four-byte big-endian length followed by exactly that many bytes. Lengths from zero through 16 MiB are valid. Reject larger lengths before allocating. The reader awaits `near.receive` before delivering the next frame. The writer awaits one socket write at a time with no pending send queue, so legal 4–16 MiB frames can proceed directly. The rutis 0.23 session outbound queue has its own budget. Non-Rust callers cannot configure that budget. Rust callers can set `SessionOptions::outbound_limit` in bytes.

EOF at a frame boundary ends the connection normally. An incomplete header or payload fails with `truncated-frame`, an oversized frame with `frame-too-large`, and a reset retains its transport error code. Closing either direction releases the whole socket. Unload interrupts pending I/O. TCP carries bytes and EOF. WebSocket carries its own 4002/4003 close codes.

Protocol 5 supports chunking for transports that advertise a message limit. In 0.23.0 the `FarSender` created by `sessions.open(far)` inherits `max_message() = None`. An encoded session message over 16 MiB can therefore reach the bridge in a single send and close that connection with `frame-too-large`. The host integration test covers a 4 MiB binary round trip and this failure for a 17 MiB value while another client remains connected. [Upstream sender](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/session/plugin/open.rs) · [Channel limit](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/channel/mod.rs)

## Dial, listen and reconnect

- Both Go plugins use `127.0.0.1`. A listener may bind port zero and reports its assigned port. Both ends verify peer certificates. Only locally authenticated mutual TLS sockets reach the bridge. Isolated framing tests may use raw loopback TCP.
- The dial row preserves #11's single-attempt default. With `reconnect: true`, the adapter initiates the first attempt and creates a fresh socket and session after disconnect. Old frames are never replayed. Transient network failures back off exponentially from 0.5 to 30 seconds. A connection lasting 60 seconds resets the delay. Known certificate rejection waits 30 seconds. A known incompatible transport protocol stops retries. Non-Rust callers lack the rutis handshake result and treat an unknown disconnect as transport loss.
- The listener accepts multiple connections and opens a session for each. Handshakes and reads proceed independently. Unload closes the listening socket, sockets still negotiating TLS and admitted connections. Listener resource errors retry after a delay.
- The first session under a row registers its addressing handle. Non-Rust `open` does not expose the greeted ID. A Rust listener can use `Session::handshake` and mount a replacement after the old session unloads. [Rust example](../../examples/rust-listen/src/lib.rs)

## Reverse-tunnel trust boundaries

The operator runs the tunnel tools. They supply the TCP route between dialer and listener and share the same bridge. A frp/rathole token, an ngrok account or an SSH credential controls who may establish the tunnel. A client reaching the forwarded port still needs end-to-end admission. The examples bind to loopback and retain mutual TLS inside the tunnel. Binding an external address changes who can reach the port.

| Route | Admission and verification |
| --- | --- |
| frp / rathole | A credential protects the control connection. Local acceptance uses a real frpc/frps TCP forward. |
| OpenSSH remote forwarding | The client verifies the SSH host key and authenticates with a user key. The loopback test uses a real remote forward. |
| ngrok TCP | An account creates a managed ingress. Address, access policy and quotas depend on the provider. |
| Cloudflare Access | The controlled ingress provides local TCP. [Adapter design](https://github.com/arcships/rutis-network/pull/15) |

## Acceptance

Loopback cases cover split/coalesced frames, empty frames, 1,000 messages in order, the 16 MiB boundary, slow peers, EOF/reset, independent sessions, denied admission, reconnect and unload. Exercise bidirectional framed traffic through both frp and SSH. The Rust example exercises same-ID replacement and the Rust limit test checks the session budget. See the [implementation guide](../guide/link-plugins.en.md) for commands and configuration.
