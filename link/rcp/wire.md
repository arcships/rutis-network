# RCP/1 wire profile

[简体中文](wire.zh-CN.md)

RCP/1 runs on an ordered WebSocket connection. Every handshake message and record occupies one binary WebSocket message. The relay can see routing tickets, sizes, timing, the wire version, ephemeral public keys and encrypted records. Each endpoint obtains its role, private key, pinned peer key, grant binding and expiry from its local caller. Account authorization happens before dialing. Rutis session identity is the endpoint named by the subsequent session Hello.

## Pairing

Use `Noise_KK_25519_AESGCM_SHA256`. The client is the Noise initiator. Both static public keys are known before pairing. Generate a fresh ephemeral X25519 key per connection on each side. The Noise prologue is the four bytes `52 43 50 01` followed by the credential binding bytes. See the [Noise protocol specification](https://noiseprotocol.org/noise.html).

1. The client sends the four-byte version prefix followed by the first Noise handshake message.
2. The daemon authenticates it and sends the same prefix followed by the second Noise handshake message. Each Noise payload contains two unsigned big-endian 32-bit integers, the sender's maximum message size in bytes (1 through 2³⁰) and heartbeat interval in milliseconds (100 through 300000). Each prefixed handshake message is exactly 60 bytes.
3. Use the smaller message limit and larger heartbeat interval. Noise Split produces AES-256-GCM keys in client→daemon, daemon→client order. The client sends an encrypted READY record, the daemon verifies it and returns its own READY. Each endpoint checks the grant expiry and opens a rutis session after verifying peer confirmation. A replay of the client's first message cannot admit a session.
4. The Go handshake deadline is the earlier of 10 s and grant expiry. A rejecting peer sends a five-byte binary message then closes. `52 43 50 45 01` means version mismatch, and `52 43 50 45 02` means credential rejection. Relays can forward these pre-admission codes unchanged. Only encrypted READY proves successful admission. There is one pairing exchange per connection.

## Records

After pairing, RCP uses the split keys with its own message-sized record envelope. The clear 16-byte header is `u32be(12) || 0x00000000 || u64be(counter)`. The last 12 bytes are the GCM nonce. Associated data is the complete header. The encrypted plaintext is a one-byte kind followed by the data bytes, and GCM appends its 16-byte tag. Total record length equals payload length plus 33. Each direction has its own counter starting at zero for READY. Require the exact next counter, authenticate before delivery, then increment. A new connection starts with fresh keys.

| Kind | Content |
| --- | --- |
| 0 | READY, pairing only |
| 1 | One complete rutis message, including a possible empty message |
| 2 / 3 | PING / PONG |
| 4 / 5 / 6 | Session close with reason done / replaced / rejected |

Control records have zero data bytes. Invalid tags, replayed or skipped counters, unknown kinds, unexpected READY and malformed records terminate the connection. The reader enforces the negotiated limit. The heartbeat uses encrypted records end to end. Answer PING with PONG. Close after three negotiated intervals without an incoming record while waiting on the socket. Message boundaries are preserved and the session owns large-value chunking.

## Failure mapping

| Condition | Reason |
| --- | --- |
| Invalid grant, HTTP 401/403 or pinned-key authentication failure | `credential-rejected` |
| Expired grant or HTTP 410 | `credential-expired` |
| HTTP 404 or no peer handshake before the pairing deadline | `peer-offline` |
| HTTP 429/502/503/504 | `relay-unavailable` |
| Version mismatch | `version-mismatch` |
| Malformed or unauthenticated record, size over limit | `invalid-frame`, `frame-too-large` |
| Established transport lost, authenticated peer close | `transport-error`, `peer-closed`, `replaced` or `refused` |

## Shared vector

All entries in [wire-v1.json](testdata/wire-v1.json) are hex bytes. Inputs include both static and both ephemeral private scalars, the shared binding, both offers and the application plaintext. Expected outputs include both prefixed handshakes, confirmations, data in each direction and close. The fixed scalars are public fixtures. Generate them with `go test -run '^TestWireVector$' -update-rcp-vector` and replay with `go test -run '^TestWireVector$'`. At client counter 1, opening `clientDataInvalidTag` must fail with `invalid-frame`. Replaying `clientData` after accepting it has the same outcome. The vector also contains both pre-admission rejection messages.
