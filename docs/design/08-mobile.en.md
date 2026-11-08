# Mobile device links and remote-control services: design

Status: draft. Date: 2026-11-08. Baseline: rutis 0.21.0.
Issue: [#8](https://github.com/arcships/rutis-network/issues/8). Depends on [#1](https://github.com/arcships/rutis-network/issues/1), [#6](https://github.com/arcships/rutis-network/issues/6).

## 1. Goal

Separate the device link from remote control:

- **8a device links**: ADB, usbmuxd, WebRTC data channel bring a device into rutis.
- **8b remote-control services**: screen/input/audio/clipboard are application services used through rutis services/events, not the link layer.

## 2. 8a Device links

### 2.1 ADB (Android)

```text
host link/tcp ── 127.0.0.1:<local> ── adb forward tcp:<local> tcp:<device> ── device
```

- `adb forward tcp:<port> tcp:<port>` / `adb reverse` gives local TCP; use #6's bridge.
- Admission: ADB key + device authorization (USB debugging); do not pass ADB identity to the session.
- Production limit: ADB debugging authorization is not for end users; document the dev/test boundary.
- The device must run a rutis runtime/bridge; `adb reverse` suits a device dialing the host.

### 2.2 usbmuxd (iOS)

- `iproxy <local> <remote>` gives local TCP; use #6's bridge.
- Admission: iOS trust/pairing; do not pass pairing identity to the session.
- Limits: needs macOS/Xcode; document app distribution/debug boundaries and USB/Wi-Fi pairing differences.

### 2.3 WebRTC data channel (optional, prototype first)

- A data channel is a generic message channel; one channel = one session.
- Signaling comes from an existing link (SSH/Tailscale/Cloudflare/gRPC); do not invent another.
- Admission: DTLS identity + application token; do not use the DTLS fingerprint as the session id.
- STUN/TURN: document relay dependency, privacy and cost; browser/mobile permissions.
- 0.21 async `send`: data-channel write blocking applies backpressure through async `send`.

### 2.4 Listen side

- Rust uses `Session::handshake` for same-id replacement; non-Rust `open(far)` does not expose the id, and `session_key(row)` is first-wins, so a second session under the same row registers no handle. Non-Rust listeners do not support same-id replacement and must be Rust (O1). Do not write "coexist".
- Non-Rust also cannot get structured handshake errors; before O1 it falls back to transport backoff and must not treat an ADB/iproxy/WebRTC disconnect reason as a handshake result.
- Do not parse `Hello`; do not substitute a device name/ADB serial for the id.

## 3. 8b Remote-control services

Remote control is an application. Suggested minimal shape (draft, not final):

```ts
interface Screen {
  capture(format: 'png' | 'jpeg'): Promise<Uint8Array>
  stream(config: { fps: number; quality: number }): AsyncIterable<Frame>
}
interface Input {
  pointer(event: { kind: 'move' | 'down' | 'up'; x: number; y: number; button?: number }): Promise<void>
  key(event: { kind: 'down' | 'up'; code: string; modifiers?: string[] }): Promise<void>
  text(text: string): Promise<void>
}
```

- Permissions, errors and backpressure for `screen` / `input` are defined separately; do not mix them into link lifecycle.
- A remote-control plugin may use RDP/VNC/SPICE/RustDesk/scrcpy/WebRTC; other plugins depend only on the service.
- Audio/clipboard come later; screen + input first.
- Unload the service correctly; when the session goes, its remote-control plugin (a child plugin) unloads.
- Do not put screen frames into ordinary session request/reply; use a stream/events to avoid a large queue.

## 4. Lifecycle

- Device link closes → `near.end`; session unloads → close the ADB/iproxy/WebRTC connection.
- Reconnect lives in the adapter: 0.5 s → 30 s exponential; an unauthorized device/auth rejection retries every 30 s; a protocol mismatch stops; the backoff resets after 60 s connected. An 8b screen/input permission denial is not retried; it is an application-layer error, not the link reconnect policy.
- 0.20 unload order: register child/connection cleanup once; do not register or wait for another cleanup.
- 0.21 async `send`: write blocking applies backpressure; the bridge queue is bounded.

## 5. Errors

| Case | Handling |
| --- | --- |
| ADB missing/device unauthorized | Report `adb devices` state and the authorization steps |
| `iproxy` missing/device not paired | Report the tool/pairing error |
| WebRTC signaling failure | Report the signaling address and SDP state |
| TURN unavailable | Report relay failure; do not silently downgrade |
| Screen/input permission denied | Report it; do not retry |
| Frame too large/backpressure | Report the limit; define the frame-drop policy |

## 6. Security boundary

- ADB/iOS pairing is the admission boundary; rutis trusts what the device lets in.
- Remote control needs its own authorization; a session alone must not grant input.
- Do not log screen content/keystrokes/clipboard; log metadata only.
- WebRTC DTLS identity does not replace the session `Hello` id.

## 7. Tests

- Android: `adb forward` + `link/tcp` runs a session; device plugins appear as `<id>:<plugin>`.
- iOS: `iproxy` + `link/tcp` (with macOS/Xcode); document limits.
- WebRTC: loopback prototype; document signaling/TURN.
- Remote control: minimal screen/input; permission denial, backpressure, unload.
- Disconnect/device unplug: session unloads with a clear error; link auth/authorization rejection every 30 s, protocol mismatch stops, backoff resets after 60 s connected.
- CI does not depend on a real device; manual e2e steps live in the docs.

## 8. Non-goals

- No RDP/VNC/SPICE as a generic link; RDP DVC is mentioned only as an application-protocol exception, not a link layer.
- No reimplementation of RDP/VNC codecs.
- No full remote-control product first.
- No ADB/WebRTC identity as session identity.
- No screen/input inside `open(far)`.

## 9. Open questions

- O1: How does the Android runtime run on the device? Reuse `design-android-runtime` or a separate app?
- O2: Feasible path for non-jailbroken iOS (embedded rutis runtime / Network Extension)?
- O3: Which link should WebRTC signaling use by default?
- O4: Does the minimal screen/input API need video encoding (H.264/VP8), or JPEG frames first?

## Links

- Issue [#8](https://github.com/arcships/rutis-network/issues/8)
- [Contract design](01-link-plugin-contract.en.md)
- [TCP bridge design](06-link-tcp.en.md)
- [iroh design](09-link-iroh.en.md)
- rutis `docs/design-android-runtime-2026-10-12.md`
