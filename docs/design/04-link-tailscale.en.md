# Go Tailscale link plugin

[中文](04-link-tailscale.md)

`go:link-tailscale` connects two rutis 0.23.0 hosts on a tailnet. By default, the row uses the signed-in system `tailscaled`. Configure `tsnet` on a row to give it a separate node identity. The tailnet access policy and the listener's `allowPeer` govern admission. The session handshake supplies the remote rutis ID. [Tailscale access controls](https://tailscale.com/docs/features/access-control) · [tsnet](https://tailscale.com/kb/1244/tsnet)

## System mode

Build from the repository root with Go 1.27.4 or later. Install the binary in `plugins/go` on each host and set `"runtimes": {"go": {"dir": "plugins/go"}}` in both `rutis.json` files.

```sh
mkdir -p plugins/go
go -C link/tailscale build -o ../../plugins/go/link-tailscale ./cmd/link-tailscale
```

Suppose the tailnet nodes are named `alpha` and `bravo` and the access policy permits TCP from alpha to bravo on port 7443. Each plugin process needs access to the local Tailscale API. A system listener needs a tailnet IP that the OS can bind. Add this listener to bravo's `rows`.

```json
{"id":"from-alpha","name":"go:link-tailscale","config":{"listen":":7443","allowPeer":"alpha"}}
```

Add this dialer to alpha's `rows`.

```json
{"id":"to-bravo","name":"go:link-tailscale","config":{"dial":"bravo:7443"}}
```

Both `dial` and `allowPeer` accept a node IP, MagicDNS short name or full name. The dialer selects a node address from the daemon's network map. `:7443` binds only the first local tailnet IP. For IPv6, specify `[local-tailnet-IPv6]:7443`. Port `0` logs the assigned address. For each incoming connection the listener looks up its TCP source with WhoIs, verifies the IP belongs to that node, then checks `allowPeer`. A fresh connection from the admitted node retires the previous transport before opening the next session. The plugin does not parse the rutis Hello.

## Embedded node

Add the following field to either row's `config`. Give each row its own hostname and private absolute state directory. Supply an auth key through the plugin process's `TS_AUTHKEY` environment for initial enrollment. Restarts use the stored node identity. Unloading an embedded row shuts down its tsnet node. In system mode, unloading releases the plugin's listener and connections.

```json
{"tsnet":{"hostname":"rutis-bravo","stateDir":"/var/lib/rutis/tailscale/bravo"}}
```

Use the embedded node's actual tailnet name or IP in the other row's `dial` or `allowPeer`. Each end can choose its mode independently.

## Manage a row from another plugin

Inject `rutis/loader` in the management plugin. This example creates and starts the dialer. Keep its returned row ID for subsequent operations.

```ts
const loader = ctx.use('rutis/loader')
const id = await loader.create({
  id: 'to-bravo', name: 'go:link-tailscale', config: { dial: 'bravo:7443' }
})
```

Use `loader.status()` for the row's load state, `loader.reload(id)` to restart it and `loader.remove(id)` to stop and remove it. To start it again, call `create` with the saved configuration. For static rows, edit and reload `rutis.json`. A bind failure fails the listener row. Dialers retry in the background with exponential backoff from 0.5 to 30 seconds, resetting after a minute of connection. A running row may be retrying. Call a remote service to verify availability.

## Diagnostics and verification

| Observation | What to check |
| --- | --- |
| `tailscaled-unavailable` / `tailnet-not-ready` | Local daemon, login state and LocalAPI permissions. |
| `peer-not-found` / `node-key-expired` | Node name, visibility in the network map and node key expiry. |
| `tailnet-dial` / timeout | Target port, listener, device connectivity and access policy. A dropped ACL packet can surface as a timeout. |
| `peer-not-allowed` / `unknown-peer` | Configured listener peer and the source of the connection. |
| `path=direct` / `DERP` / `peer-relay` | The path observed when the stream was mounted. A pending observation shows `unknown`. Relaying can affect latency and throughput. Use `tailscale status` for the current path on system nodes. |

The shared Go TCP bridge uses a four-byte big-endian length prefix and a 16 MiB frame limit. Reconnection starts a fresh session without replay. The rutis 0.23 Go API exposes neither the peer's rutis Hello ID nor a session outbound queue limit. [TCP bridge](../../link/tcp/bridge.go) · [Tailscale CLI](https://tailscale.com/docs/reference/tailscale-cli)

Run these checks from the repository root. The integration test uses rutis-host 0.23.0 from PATH or `RUTIS_HOST`. It starts a local test control server, DERP and two independent nodes, then checks remote calls, removal, recreation, admission and reconnection. Set `RUTIS_TAILSCALE_SYSTEM_TEST=1` to additionally exercise two host processes using the local enrolled system node.

```sh
go -C link/tailscale test -race ./...
go -C link/tailscale test -tags rutishost -run TestHosts ./...
```
