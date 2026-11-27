# Managing a TCP plugin connection

A plugin in any language supported by rutis can use this interface. The Go binary provides TCP dial and listen. Each managed instance publishes its control object as a named service. It stays usable while disconnected. This API targets rutis 0.23.0. [中文](06-link-tcp-integration.md) · [Build and deployment](../guide/link-plugins.en.md)

## Mount the plugin through the host

Place the Go binary in `plugins/go` and register its runtime and TCP row in `rutis.json`. The host can now load the instance. This example waits for the caller to set a target. To dial at load time, supply `host` and `port` in `config`.

```json
{
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [
    {
      "id": "wire",
      "name": "go:link-tcp-dial",
      "isolate": {
        "office-tcp": "management"
      },
      "config": {
        "controlService": "office-tcp",
        "ca": "/etc/link/peer-ca.pem",
        "cert": "/etc/link/client.pem",
        "key": "/etc/link/client.key"
      }
    }
  ]
}
```

## Call the service from an existing plugin

Merge these fields into the existing local caller's row, keeping its runtime, entry point, other dependencies and scope mappings. Use the caller's rutis SDK to acquire `office-tcp` by name. Plugins that only use a remote application service can continue to inject that service directly.

```json
{
  "inject": ["office-tcp"],
  "isolate": { "office-tcp": "management" }
}
```

For example, when a user selects a server, pass the object `{"host":"office.example.com","port":7443}` to `configure`. Call `status()` for a snapshot, then pass its `cursor` string to `waitForChange`. Call `stop()` when the user disconnects. The methods, arguments and results are shared across SDKs.

**Keep the control service and caller in the same local isolation scope.** The example maps the same service name to the same `isolate` label on both rows. The host exports ordinary root-scope services to connected peers. [Host export rules](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/session/plugin/services.rs) · [Integration tests](../../link/tcp/node/test/control.test.mjs)

## Mount from plugin code at runtime

When the host declares the TCP row, the manager can inject its `controlService` directly. A manager that creates the TCP row at runtime can use `go:link-tcp-attach` from the same Go binary. This optional row delivers the TCP control handle whenever both the named control service and the manager's receiver are available.

Declare the following isolated group. Put the manager in the group or give its `office-owner` service the same isolation label. A holder of the control object can operate the link. Keep both services within the local management scope.

```json
{
  "id": "local-links",
  "group": true,
  "isolate": { "office-tcp": "management", "office-owner": "management" },
  "config": [
    {
      "id": "deliver",
      "name": "go:link-tcp-attach",
      "inject": ["office-tcp", "office-owner"],
      "config": { "controlService": "office-tcp", "controlReceiver": "office-owner" }
    }
  ]
}
```

The manager injects `rutis/loader` and provides `office-owner.attach(handle)`, storing each handle it receives. For the initial mount, call `rutis/loader.create(options, "local-links")` from an event or service method after the manager has started. Use the options below. The TCP row publishes `office-tcp` and the plugin declares its own transport dependencies. `rutis-host run` persists the new row in the host configuration. [Loader API](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-loader/src/loader/service.rs)

```json
{
  "id": "wire",
  "name": "go:link-tcp-dial",
  "config": {
    "controlService": "office-tcp",
    "autoStart": false,
    "ca": "/etc/link/peer-ca.pem",
    "cert": "/etc/link/client.pem",
    "key": "/etc/link/client.key"
  }
}
```

`create` returns the row ID. Receipt of `attach` signals that the control object is ready. Prepare the receiver before calling `create` and allow the callback to run while waiting. Go's `Attach` can accept a `*advanced.Service`. Other languages receive their SDK's remote object proxy. Give each independently managed connection its own service names and attachment row. [Go creator and receiver](../../link/tcp/test/fixtures/mounter/main.go)

The TCP row keeps its socket, runtime configuration and status cursor across manager restarts. When the manager provides `office-owner` again, `deliver` calls `attach` again. Store that handle to resume control of the existing row. Other consumers can keep using the connection when a business plugin restarts. Unloading the TCP row closes its socket and invalidates its handle. Reloading it creates a fresh instance using deployment settings. An explicit `stop()` closes the transport and leaves its control service available.

To migrate the local `controlReceiver` draft, move that field and the receiver injection from the TCP row to `deliver`, then set `controlService` on the TCP row.

## Methods

| Method | Behavior |
| --- | --- |
| `configure(options)` | Apply a patch. A dialer accepts `host`, `port` (1–65535), `servername` and `reconnect`. A listener accepts `port` (0–65535). Omitted fields retain their values. Pass `null` for `host` or `servername` to clear an override. Validation precedes any change. An invalid or identical patch preserves the current connection. |
| `start()` | Start once a port is configured. Repeated starts share the active attempt. An explicit start after failure tries again. A missing port returns an error. The call returns when the request is accepted. Read the connection or bind result from the snapshot. |
| `stop()` | Cancel the active attempt and retry timer, then release the dial socket or the listener and all its clients. Returns after transport cleanup. Repeated calls are safe, and runtime settings remain available. |
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
- Changing a running instance's host, port or TLS name ends the old connection and backoff, then attempts the new target, including with `reconnect: false`. Rebinding a listener releases all its clients. A busy port yields `failed` and `EADDRINUSE`. Change the port or call `start()` to retry.
- Changing only retry policy preserves a healthy socket. Disabling retries during backoff cancels the next attempt. Enabling them after a transient failure resumes dialing. Delays double from 0.5 to 30 seconds and reset after a connection lasts 60 seconds. Certificate rejection or invalid local credentials wait 30 seconds. A known incompatible TLS transport stops automatic attempts. Each new connection starts a fresh session. Old calls are never replayed.
- After `stop()`, changing the endpoint only updates the instance's settings. Its next `start()` uses the new target. Runtime edits live in memory. Reload and host restart restore deployment settings.
- Dial supports DNS, IPv4 and IPv6. TLS verifies the explicit `servername`, otherwise the explicit `host`. A port-only row still dials `127.0.0.1` and verifies `localhost`. Listen binds to IPv4 loopback. Use a reverse tunnel when forwarding is required. [Transport design](06-link-tcp.en.md)

## Switching a row that hosts remote plugins

Use `configure` to switch endpoints for ordinary cross-host service calls. If the host also runs remote plugins through `wire:<plugin>`, rutis 0.23.0 acknowledges the end of input through `near.end` while old plugins may still be unloading. The old session can still own the row's route when the new socket connects.

To move these plugins reliably to another host, await `rutis/loader.remove("wire")`, then call `rutis/loader.create(options, "local-links")`. Reuse `id: "wire"` and the original isolation group. Supply the new endpoint and desired settings in `options.config`. The host releases the old row and mounts the existing `wire:<plugin>` rows on the new peer. The new TCP instance has its own control handle and cursor. A successful service call confirms application readiness. [Release-host integration test](../../link/tcp/node/test/control.test.mjs)

## Develop and load the TCP plugins

From `link/tcp/node`, run `npm ci`, then `npm run dev:setup` once. Setup uses Go and OpenSSL to build a named loopback peer, create a seven-day local mTLS identity and write the two development configurations. Set `PORT` during setup to change the peer’s default port of 17443. Start the peer with `npm run dev:peer` and keep it running. In a second terminal, also in `link/tcp/node`, choose the target you are editing.

| Target | Start | Call after the initial connection or a source edit |
| --- | --- | --- |
| Node/TS | `npm run dev` | `node_modules/.bin/rutis-host call --config dev witness.echo 37` |
| Go | `node_modules/.bin/rutis-host dev ..` | `node_modules/.bin/rutis-host call --config .. witness.echo 37` |

The expected reply is `["loopback",37]`. Go edits rebuild the binary. The 0.23 Node runtime caches imports below the mounted entry, so esbuild watches the local TS modules and atomically replaces a single dev entry after each successful build. The host reloads that row; the dialer establishes a fresh connection.

Run `npm pack` from the Node package to build the runtime ESM and declarations into a local tarball. Install it alongside `@arcships/rutis-runtime@0.23.0` in the consumer’s Node project. Set `runtimes.node.project` to that directory and the row’s `name` to `rutis-network-tcp-reference`. Its configuration is `port`, `ca`, `cert` and `key` for a loopback mTLS connection. Existing source rows can still use `link/tcp/dial.ts`. When migrating a previous checkout, move its old `link/tcp/node_modules` aside before installing in `node/` so both entry points use the same runtime dependencies. Run `npm run test:delivery` to exercise source edits and an external tarball installation.
