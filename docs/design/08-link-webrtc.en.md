# WebRTC connections for Web ↔ Web and Client ↔ Web

[中文](08-link-webrtc.md) · [Device links issue #8](https://github.com/arcships/rutis-network/issues/8)

Implemented for rutis 0.23.0, session protocol 5. A Web endpoint runs inside a browser page. The first native Client is an application embedding rutis or a rutis-host process running a Go/Pion adapter. One pairing connects two endpoints. Both can provide and call services. The offerer creates the DataChannel.

| Scenario | User action | Observable outcome |
| --- | --- | --- |
| Web ↔ Web | Two users accept a pairing invitation in the same application in their respective browsers. | Page A calls a service on page B. B calls A over the same connection. Each machine needs only a browser. |
| Client ↔ Web | A user selects an authorized desktop application or device in the web page. | The page calls the device plugin and receives a callback. Restarting the device's business plugin preserves the connection for the next call. |

## From pairing to the first call

1. The application authenticates users, lets them select a peer or accept an invitation, and assigns a shared `pairId` to two authorized endpoints. Its existing HTTPS or WebSocket service can carry signaling. Either the native application or the browser can take either role.
2. Each endpoint acquires a pairing grant from its local signaling provider. The provider assigns one offerer, one answerer and one short-lived `attemptId`. The offerer creates the channel and publishes an offer containing its ICE candidates. The answerer returns an answer. Each side includes gathered ICE candidates in a single SDP publication. A browser with a usable relay can publish while other interfaces finish gathering.
3. ICE selects a path. DTLS verifies the certificate fingerprint bound to the authenticated SDP. Each adapter checks the current grant, peer and channel before handing its channel to a session. Transport status then indicates connectivity. A successful application RPC establishes service readiness. Rutis handles Hello, service discovery and calls. [Session handoff contract](01-link-plugin-contract.en.md) · [WebRTC specification](https://www.w3.org/TR/webrtc/)

## Run the example

Use Go 1.26 and a Node.js version supported by the build tools (`^22.18.0 || ^24.11.0 || >=26.0.0`) on the build machine. Run the following commands from the repository root. They produce the browser module at `dist/link-webrtc.mjs` and one Go executable containing both the transport and the HTTP signaling provider.

```sh
cd link/webrtc
npm ci --prefix web
npm run build --prefix web
mkdir -p plugins/go
go build -o plugins/go/link-webrtc ./cmd/link-webrtc
go build -o dist/turn-example ./example/turn
export TURN_SECRET="$(openssl rand -hex 32)"
export STUN_URL="stun:127.0.0.1:3478"
export TURN_URL="turn:127.0.0.1:3478?transport=udp"
export PAIR_A_TOKEN="$(openssl rand -hex 24)"
export PAIR_B_TOKEN="$(openssl rand -hex 24)"
printf 'A: %s\nB: %s\n' "$PAIR_A_TOKEN" "$PAIR_B_TOKEN"
(
  ./dist/turn-example 3478 &
  relay_pid=$!
  trap 'kill "$relay_pid" 2>/dev/null || true' EXIT
  node example/server.mjs
)
```

These commands start an authenticated loopback TURN server and the pairing page. Open `http://127.0.0.1:8080` in two browsers. Enter distinct page IDs such as `page-a` and `page-b` and the respective A and B credentials. Connect both, enter the other page’s service name and click **Call echo**. The example uses the page ID as its service name. For separate devices, serve the page and `/signal` under an HTTPS origin reachable from both endpoints.

**Use your own TURN server.** Replace the loopback service above. Set `TURN_URL` (e.g. `turn:turn.example.org:3478?transport=udp`) and `TURN_SECRET` before starting the example. The TURN server must accept shared-secret credentials and expose its listener and relay port range to both endpoints. With coturn, enable `use-auth-secret` and set `static-auth-secret` to the signaling server’s `TURN_SECRET`. The signaling server issues a credential per member. Its lifetime defaults to one hour and can be set with `TURN_TTL_SECONDS`. Both endpoints request fresh credentials halfway through that lifetime. The shared secret stays on the server. Select **Require TURN** on both ends. A working path shows `connected · relay`. Policy `all` also permits direct ICE paths, with optional `STUN_URL` for address discovery. [coturn authentication and ports](https://github.com/coturn/coturn/blob/master/examples/etc/turnserver.conf)

## Browser development and packaging

With the same pairing credentials and optional TURN settings as the example, run `npm run dev --prefix web` from `link/webrtc`. It starts the example and same-origin signaling. Saving browser TS triggers tsdown to rebuild the ESM and Vite to refresh open pages on success. A failed build prints its error and keeps serving the last good bundle. Saving the Node pairing module restarts the example server. Reconnect on the refreshed page to open a new session.

Download `arcships-rutis-link-webrtc-<version>.tgz` from a [WebRTC release](https://github.com/arcships/rutis-network/releases?q=link%2Fwebrtc) that includes a browser npm package. The package version matches the release tag. To build a local tarball, run `npm ci && npm pack` in `link/webrtc/web`. In the consuming TypeScript project, install the file using its actual version:

```sh
npm install ./arcships-rutis-link-webrtc-0.1.0.tgz
```

Import `createWebRtcLink`, `createPageEndpoint`, `httpSignaling` and their types from `@arcships/rutis-link-webrtc`. The package exports the browser ESM and its declaration entry point. A plain HTML page can serve the included `dist/link-webrtc.mjs` directly. Deploy the signaling and TURN services separately.

To verify a specific tarball, run `WEBRTC_TARBALL=/absolute/path/package.tgz npm run test:package` in `link/webrtc/web` with Chromium installed (`npx playwright install chromium`). An independent app installs the tarball, type-checks with `strict: true` and `skipLibCheck: false`, and builds with Vite. Two Chromium pages then exchange an echo and a callback through authenticated TURN. `npm run test:delivery` also exercises development reload and build-error recovery. [TypeScript consumer](../../link/webrtc/web/test/fixtures/consumer/app.ts)

## Integrate a native host and a page

Save this as `link/webrtc/rutis.json`. Run `web/node_modules/.bin/rutis-host run rutis.json` from that directory with member A’s token in the host environment. The page uses member B’s token. Mount business plugins in separate rows so their services can reload on the existing connection. Use the deployed HTTPS signaling URL for remote peers.

```json
{
  "id": "device",
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [
    {
      "id": "signaling",
      "name": "go:link-webrtc-signaling",
      "isolate": { "app-pairing": "local" },
      "config": {
        "service": "app-pairing",
        "url": "http://127.0.0.1:8080/signal",
        "tokenEnv": "PAIR_A_TOKEN"
      }
    },
    {
      "id": "device-wire",
      "name": "go:link-webrtc",
      "inject": ["app-pairing"],
      "isolate": { "app-pairing": "local", "device-link": "local" },
      "config": {
        "pairId": "desk-and-page",
        "signalingService": "app-pairing",
        "controlService": "device-link",
        "iceTransportPolicy": "relay"
      }
    }
  ]
}
```

The plugin declares its own `rutis/sessions` injection. A management plugin in any rutis language can access the control service with row fields `"inject": ["device-link"]` and `"isolate": { "device-link": "local" }`. Both signaling and management services use host-private scopes. A static deployment can omit `controlService`.

Import the built module in the page. Here `memberToken` comes from the application’s authenticated pairing flow. The application can also supply its own signaling provider. The defaults are `autoStart: true`, `reconnect: true` and `iceTransportPolicy: "all"`.

```js
import { createPageEndpoint, createWebRtcLink, httpSignaling } from './link-webrtc.mjs'

const page = createPageEndpoint('page-b')
const remove = page.provide('display', { echo: value => value })
const link = createWebRtcLink({
  sessions: page.sessions,
  signaling: httpSignaling('/signal', { headers: { Authorization: 'Bearer ' + memberToken } }),
  config: { pairId: 'desk-and-page', iceTransportPolicy: 'relay', autoStart: false }
})
await link.start()
```

Both endpoints can publish and call services. For a native `sensor.echo` service, check `page.remoteServices()` for `sensor` and call `await page.call('sensor', 'echo', value)`. A native plugin can inject the page’s `display` service. Call `remove()` to withdraw that service or `await link.stop()` to close the whole connection.

`createPageEndpoint(id, { onServiceError })` reports rejected service announcements and withdrawals as `{ service, operation, error }`. A name collision leaves unrelated calls running. When one page withdraws its conflicting provider, it can discover the other page’s service. The page advertises the services family and accepts both records of function references and object-backed services.

Callbacks passed into a page handler retain the incoming call path across `await`. For a separate service call after an asynchronous step, capture the caller on entry. This also works in browsers without native async context.

```js
page.provide('display', {
  async render(value, done) {
    const call = page.captureCall()
    await draw(value)
    await call('sensor', 'ack', value)
    return done(value)
  }
})
```

| Control method | Contract |
| --- | --- |
| `configure(patch)` | Accepts `pairId`, `iceTransportPolicy` and `reconnect`. Validate the whole patch first. Changing the pair or ICE policy while running releases the old connection and acquires a new grant. Changing only the retry flag preserves a healthy connection. |
| `start()` / `stop()` | Idempotent. Start waits for the other endpoint. Stop cancels signaling waits and backoff, closes the channel and awaits local transport cleanup. A stopped adapter applies a new target on the next start. |
| `status()` / `waitForChange(cursor)` | Return `desired`, `phase`, `route`, `pairId`, `retryCount` and `cursor`, with `lastError` on failure and `retryAt` during backoff. A stale cursor returns the current snapshot immediately, including after a management plugin remount. |

The normal phases are `pairing → negotiating → connected`. Other phases are `stopped`, `retrying` and `failed`. Derive `route` from the selected ICE candidate pair. Report `relay` if either candidate is relayed, `direct` if both are known and neither is relayed, and `unknown` if statistics are unavailable or the connection ended. ICE policy `all` allows path selection. Policy `relay` requires TURN and reports `turn-unavailable` when usable TURN configuration is missing. Expose path categories and error codes in diagnostics. [WebRTC statistics](https://www.w3.org/TR/webrtc-stats/)

## Application signaling interface

This is a local service contract. The application chooses the network URL, login method, rooms and invitation UI. The configurable logical address is `pairId`. The provider supplies STUN/TURN addresses and temporary TURN credentials per attempt.

| Method | Input and result |
| --- | --- |
| `acquire({pairId, after, waitMs, renewOf?})` | Returns `{attemptId, role, expiresAt, iceServers}` or `null`. Pass null for `after` initially or the last retired attempt ID on reconnect. Calls are retryable. Both authorized members receive the same attempt, complementary roles and their own ICE configuration. |
| `publish({pairId, attemptId, type, sdp})` | The offerer publishes the offer and the answerer publishes the answer. Republishing identical content is idempotent. Reject replacement of published content. |
| `receive({pairId, attemptId, type, waitMs})` | Reads the other endpoint's message for this attempt. Returns `{type, sdp}` or `null`. The message can be read again until expiry. Calls have bounded waits. Discard outstanding replies after stop. |

A provider offering renewable TURN credentials includes an RFC 3339 UTC `refreshAt` in both initial grants. At that time each endpoint calls `acquire` with the original `attemptId` as `renewOf` and its most recently negotiated ID as `after`. Return `null` until both members ask for the next round. Then return a fresh ID, the same `renewOf` and role, new ICE credentials, a negotiation deadline and the next `refreshAt`. The peers exchange SDP for the new ID and restart ICE on the existing PeerConnection. This preserves the DataChannel, rutis session and outstanding calls. Schedule renewal with enough time for both peers to gather and exchange candidates. An existing provider that omits `refreshAt` performs only the initial negotiation.

Both HTTP clients accept JSON `null` or HTTP 204 when polling has no result. An ICE server’s `urls` field accepts a URL string or an array of URL strings.

The provider authenticates signaling sources, protects SDP confidentiality and integrity, and binds the two members, roles, attempt and expiry. A room name addresses a pair. Each endpoint accepts only the intended peer's description under its current grant and rejects stale, replayed, cross-pair or wrong-role messages. Authorizing a pairing grants the peer access as a trusted rutis participant. The application consumes an invitation for first use and applies its authorization policy to reconnects. TURN credentials, raw SDP, candidate addresses and fingerprints stay within the private negotiation interfaces. An established DataChannel can continue through a signaling outage. [WebRTC security architecture](https://www.rfc-editor.org/rfc/rfc8827)

Role values are `offerer` and `answerer`. Description types are `offer` and `answer`. The RFC 3339 UTC `expiresAt` is the deadline for completing negotiation. A retry after expiry needs a fresh attempt. The provider assigns roles to authenticated members and returns the same grant for a repeated acquisition by one member. Start returns once the worker is scheduled. Use `waitForChange` to await connectivity. An endpoint with a healthy connection rejects an attempt to displace it.

## Channel, messages and cleanup

The offerer creates one binary channel with label `rutis` and protocol `rutis-link/1`, using ordered, reliable delivery. The answerer validates these properties and rejects extra channels. Both sides install receive and close handlers before opening the session. Go injects `rutis/sessions`. The browser adapter takes an `open(far) → near` binding from the page endpoint. The adapter handles opaque bytes. [Go Far/Near types](../../link/tcp/bridge.go) · [WebRTC Data Channels](https://www.rfc-editor.org/rfc/rfc8831)

Each binary DataChannel message carries one v1 record. Its total size is bounded by **the smaller of 16 KiB and the negotiated send limit**. A complete rutis message can be up to 16 MiB. These limits apply to one WebRTC send and one session delivery respectively. [SCTP message size negotiation](https://www.rfc-editor.org/rfc/rfc8841)

| First byte | Remaining bytes | Action |
| --- | --- | --- |
| `0x01` | Four-byte unsigned big-endian total length, followed by the first payload fragment | Validate length before allocating. A zero-length message has only the length field after the tag. |
| `0x02` | One or more continuation bytes | Append to the current message. Call `near.receive` once when the exact declared length has arrived. |
| `0x03` | Empty | Acknowledge once after `near.receive` succeeds. The sender can then start its next message in this direction. |

Deliver a complete first record immediately. For a nonempty message the first record must contain at least one data byte. The sender checks that the negotiated limit accommodates it. End the connection on text, unexpected or out-of-order records, overflow or truncation. Discard partial messages on close. Each direction has at most one message in flight. Sending an acknowledgment proceeds independently of a local sender waiting for one. Both implementations pause and resume against send-buffer watermarks. This propagates slow-consumer pressure at the cost of one acknowledgment wait per message. Rutis 0.23 owns the session's total outbound queue. Larger application values use application-level chunks and reassembly. [TCP large-call contract](06-link-tcp.en.md)

During session opening, hold at most the first complete message until `near` becomes available, then deliver and acknowledge it. Use high and low watermarks and reserve send capacity for acknowledgments. Closing the channel, canceling or unloading the row releases waits for acknowledgments, buffer space and receive completion.

Transport closure ends the local session once. Session closure closes the channel and wakes blocked senders. Allow transient ICE `disconnected` to recover. After 10 continuous seconds disconnected or on ICE `failed`, release the old connection. With `reconnect` enabled, back off from 0.5 to 30 seconds, then use a fresh grant and PeerConnection for each retry. A denied pairing or incompatible channel enters `failed` until a valid configuration change or explicit start. Refreshing a page creates a fresh endpoint. A row starts its next attempt after cleaning up the previous transport.

## Browser endpoint and verification

The page endpoint reuses the session and codec modules from the pinned `@arcships/rutis-runtime@0.23.0` with a build-time browser platform binding. Pages explicitly export asynchronous services with `provide`. They support method calls, callbacks, binary values and service withdrawal. Requalify these modules and their protocol when upgrading rutis. The native and browser transports deliver the same wire records. [Page endpoint](../../link/webrtc/web/src/browser/endpoint.ts) · [Browser build](../../link/webrtc/web/tsdown.config.mjs)

```sh
cd link/webrtc
npm ci --prefix web
npm exec --prefix web -- playwright install chromium firefox
npm test --prefix web
go test -race ./...
```

From `link/webrtc/web`, run `npm run test:unit` for the channel, pairing and page endpoint tests in Vitest. `npm run test:integration` builds the package and runs host, browser and TURN cases with the Node test runner. `npm test` runs both.

The integration tests run stock rutis-host 0.23, Chromium, Firefox and a real loopback STUN/TURN server. Each endpoint combination exercises direct and forced-relay paths, checks both selected routes and makes bidirectional RPCs. The relay tests stop the TURN process, wait for service withdrawal, restart TURN on the same port and call again. Short-lived credential tests hold a callback across expiry, verify a new selected relay allocation and complete both the pending RPC and a new call on the same session. Coverage also includes page refresh, business-plugin reload, callbacks, binary values spanning records, the 16 MiB message boundary and backpressure. Public NAT topologies and production TURN deployments require separate validation. [Integration tests](../../link/webrtc/web/test/hosts.test.mjs) · [Browser unit tests](../../link/webrtc/web/test/unit/channel.test.mjs) · [Go boundary tests](../../link/webrtc/records_test.go)
