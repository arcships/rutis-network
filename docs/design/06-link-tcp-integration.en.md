# Managing a TCP plugin connection

A local integration can change the endpoint, start and stop the transport, and observe its state. Each managed Go dial or listen instance provides its own rutis service for the lifetime of the row, including while disconnected. This API targets rutis 0.23.0. [中文](06-link-tcp-integration.md) · [Build and deployment](../guide/link-plugins.en.md)

## Connect a management plugin

Add these rows to a host configured with the Go and management plugin runtimes. Replace `./plugins/settings.mjs` with your management plugin entry point. To control another link, install another TCP instance with its own service name.

```json
{
  "rows": [
    {
      "id": "wire",
      "name": "go:link-tcp-dial",
      "isolate": { "office-tcp": "management" },
      "config": {
        "controlService": "office-tcp",
        "ca": "/etc/link/peer-ca.pem",
        "cert": "/etc/link/client.pem",
        "key": "/etc/link/client.key"
      }
    },
    {
      "id": "settings",
      "name": "./plugins/settings.mjs",
      "inject": ["office-tcp"],
      "isolate": { "office-tcp": "management" }
    }
  ]
}
```

**Give both rows the same service name and `isolate` label.** rutis publishes ordinary root-scope services to connected peers. Omitting `isolate` on the provider exposes connection controls to peers. The host enforces isolation, the manager injects the service, and business plugins continue to inject their application services. The host suite verifies isolation and a successful remote application call on the same link. [Host export rules](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/session/plugin/services.rs) · [Integration tests](../../link/tcp/test/control.test.mjs)

A management plugin can handle a request to connect and a subsequent disconnect action:

```js
const tcp = ctx.use('office-tcp')
await tcp.configure({ host: 'office.example.com', port: 7443 })
let state = await tcp.status()
while (state.phase !== 'connected' && state.phase !== 'failed') {
  state = await tcp.waitForChange(state.cursor)
}
// When the user requests a disconnect
await tcp.stop()
```

## Methods

| Method | Behavior |
| --- | --- |
| `configure(options)` | Apply a patch. A dialer accepts `host`, `port` (1–65535), `servername` and `reconnect`. A listener accepts `port` (0–65535). Omitted fields retain their values. Pass `null` for `host` or `servername` to clear an override. Validation precedes any change. An invalid or identical patch preserves the current connection. |
| `start()` | Start once a port is configured. Repeated starts share the active attempt. An explicit start after failure tries again. A missing port returns an error. The call returns when the request is accepted. Read the connection or bind result from the snapshot. |
| `stop()` | Cancel the active attempt and retry timer, then release the dial socket or the listener and all its clients. Returns after cleanup. Repeated calls are safe, and runtime settings remain available. |
| `status()` | Return the latest snapshot immediately. |
| `waitForChange(cursor)` | Wait for the next change if the cursor matches, otherwise return the latest snapshot immediately. A cancellable call context can end the wait. After reload, reacquire the service and pass the old cursor to get the new instance's snapshot. |

### Snapshot fields

`role` is `dial` or `listen`. `desired` is `running` or `stopped`. `config` contains the configured port, explicit host and server name, and retry flag. Credentials stay in the deployment. `cursor` is an opaque instance revision. A slow observer can receive one snapshot covering multiple changes.

| `phase` | Meaning |
| --- | --- |
| `idle`, `awaiting-config`, `stopped` | A manual instance before its first start, an automatic instance waiting for a port, or an explicitly stopped instance. |
| `dialing`, `handshaking`, `connected` | Opening the socket, negotiating TLS, or locally TLS-verified and attached to `sessions.open`. Service registration or a successful call establishes application readiness. |
| `starting`, `listening` | Starting a listener or bound. While listening, `boundPort` is the assigned port and `connections` counts admitted sockets attached to the bridge. |
| `retrying`, `failed` | Waiting for a retry or finished attempting. Recovery follows the retry policy. |

`localAddress` and `remoteAddress` appear when known. `lastError` records the latest transport code, such as `ECONNREFUSED`, `certificate-rejected` or `EADDRINUSE`. `retryCount` counts retries scheduled since the attempt loop started. During backoff, `retryAt` gives the next attempt's UTC timestamp.

## Startup, retries and endpoint changes

- Managed instances default to `autoStart: true`, and managed dialers to `reconnect: true`. A row without a port provides its control service and starts once configured. With `autoStart: false`, it waits for an explicit start. Static rows omit `controlService`, require a port, and start at load. Static dial defaults to a single attempt.
- Changing a running instance's host, port or TLS name ends the old connection and backoff, then attempts the new target immediately, including with `reconnect: false`. Rebinding a listener releases all its clients. A busy port yields `failed` and `EADDRINUSE`. Change the port or call `start()` to retry.
- Changing only retry policy preserves a healthy socket. Disabling retries during backoff cancels the next attempt. Enabling them after a transient failure resumes dialing. Delays double from 0.5 to 30 seconds and reset after a connection lasts 60 seconds. Certificate rejection or invalid local credentials wait 30 seconds. A known incompatible TLS transport stops automatic attempts. Each new connection starts a fresh session. Old calls are never replayed.
- After `stop()`, changing the endpoint only updates the instance's settings. Its next `start()` uses the new target. Runtime edits live in memory. Reload and host restart restore deployment settings.
- Dial supports DNS, IPv4 and IPv6. TLS verifies the explicit `servername`, otherwise the explicit `host`. A port-only row still dials `127.0.0.1` and verifies `localhost`. Listen binds to IPv4 loopback. Use a reverse tunnel when forwarding is required. [Transport design](06-link-tcp.en.md)
