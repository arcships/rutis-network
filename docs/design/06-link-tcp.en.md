# TCP byte-stream bridge and reverse tunnels

Finalized on 2026-11-26 against rutis 0.23.0 (session protocol 5, plugin API 3). [Issue #6](https://github.com/arcships/rutis-network/issues/6) tracks scope and acceptance. [中文](06-link-tcp.md)

## Boundary and framing

SSH forwarding, frp, Tailscale, Cloudflare Access, ADB and usbmuxd can all provide a byte stream. The adapter reaches and admits the peer before handing each verified stream to Go’s `tcp.Open`. The Go bridge implements #11’s wire contract and interoperates with its TypeScript reference. The bridge owns framing, backpressure, closure and one `sessions.open(far)` per stream. rutis owns Hello, identity and service lifecycle. The existing WebSocket transport keeps its native message boundaries. [Link contract](01-link-plugin-contract.en.md)

A frame contains an unsigned four-byte big-endian length followed by exactly that many bytes. Lengths from zero through 16 MiB are valid. Reject larger lengths before allocating. The reader awaits `near.receive` before delivering the next frame. The writer awaits one socket write at a time with no pending send queue, so legal 4–16 MiB frames can proceed directly. The rutis 0.23 session outbound queue has its own budget. Non-Rust callers cannot configure that budget. Rust callers can set `SessionOptions::outbound_limit` in bytes.

EOF at a frame boundary ends the connection normally. An incomplete header or payload fails with `truncated-frame`, an oversized frame with `frame-too-large`, and a reset retains its transport error code. Closing either direction releases the whole socket. Unload interrupts pending I/O. TCP carries bytes and EOF. WebSocket carries its own 4002/4003 close codes.

On rutis 0.23.0, applications transfer large values in chunks and reassemble them in order. The integration test echoes 17 MiB of random binary data as four 4 MiB chunks and a final 1 MiB chunk on one connection. It compares the reassembled result byte for byte and makes another call on the same connection. The test uses 4 MiB to leave room for session encoding and metadata.

Session protocol 5 handles transparent chunking of a single large call. In 0.23.0, `sessions.open(far)` cannot advertise the transport's frame limit, so an encoded message over 16 MiB can close the connection. When a rutis release exposes that limit, the plugin will declare 16 MiB and the host suite will exercise a single 17 MiB call, checking that each frame fits, the complete value is delivered once and the connection stays usable. [Upstream issue and measured behavior](https://github.com/arcships/rutis/issues/1411)

## Dial, listen and reconnect

- Go listen binds to `127.0.0.1`. Dial defaults to the same address and also accepts DNS, IPv4 and IPv6 targets. A listener may bind port zero and reports its assigned port. Both ends verify peer certificates. Only locally authenticated mutual TLS sockets reach the bridge. Isolated framing tests may use raw loopback TCP.
- The static dial row preserves #11's single-attempt default. With `reconnect: true`, the adapter initiates the first attempt and creates a fresh socket and session after disconnect. Old frames are never replayed. Transient network failures back off exponentially from 0.5 to 30 seconds. A connection lasting 60 seconds resets the delay. Known certificate rejection waits 30 seconds. A known incompatible transport protocol stops retries. Non-Rust callers lack the rutis handshake result and treat an unknown disconnect as transport loss.
- The listener accepts multiple connections and opens a session for each. Each listener admits up to 32 concurrent TLS handshakes. Additional connections wait in the OS listen backlog. A completed handshake releases its slot while the session reads independently. Unload closes the listening socket, sockets still negotiating TLS and admitted connections. Listener resource errors retry after a delay.
- The first session under a row registers its addressing handle. Non-Rust `open` does not expose the greeted ID. A Rust listener can use `Session::handshake` and mount a replacement after the old session unloads. [Rust example](../../examples/rust-listen/src/lib.rs)

A managed TCP row owns its sockets and retry loop and publishes its controls through an isolated `controlService`. A manager can inject that service directly or receive its handle through a separate `go:link-tcp-attach` row. The TCP row keeps running when the manager restarts. [Control API and scope configuration](06-link-tcp-integration.en.md)

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
