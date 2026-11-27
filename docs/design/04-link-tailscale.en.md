# Go Tailscale link plugin

[中文](04-link-tailscale.md)

`go:link-tailscale` lets rutis 0.23.0 hosts discover and connect to other hosts on a tailnet. It uses the signed-in system `tailscaled` by default and also supports independent tsnet nodes. The session Hello supplies the remote rutis ID.

## Installation

Build from the repository root with Go 1.27.4 or later. Install the binary in `plugins/go` on each host and set `"runtimes": {"go": {"dir": "plugins/go"}}` in each host’s `rutis.json`.

```sh
mkdir -p plugins/go
go -C link/tailscale build -o ../../plugins/go/link-tailscale ./cmd/link-tailscale
```

## Discover workers by tag

Assign `tag:rutis-controller` to the host and `tag:rutis-worker` to each leaf. Merge the following tags and grant into the existing tailnet policy to allow the host to connect to TCP 7443 on the leaves. Apply tags to enrolled nodes or use a tagged auth key for new nodes. [Tailscale tags](https://tailscale.com/docs/features/tags) · [Access grants](https://tailscale.com/docs/features/access-control/grants)

```json
{
  "tagOwners": {
    "tag:rutis-controller": ["autogroup:admin"],
    "tag:rutis-worker": ["autogroup:admin"]
  },
  "grants": [{
    "src": ["tag:rutis-controller"],
    "dst": ["tag:rutis-worker"],
    "ip": ["tcp:7443"]
  }]
}
```

Add the discovery row to `rows` in the host’s `rutis.json`.

```json
{"id":"leaves","name":"go:link-tailscale","config":{"discover":{"tag":"tag:rutis-worker"}}}
```

Install the same plugin and add this listener to `rows` on each leaf.

```json
{"id":"from-host","name":"go:link-tailscale","config":{"allowTag":"tag:rutis-controller"}}
```

The host subscribes to changes in its local Tailscale peer map and opens one rutis session per matching leaf, keyed by stable node ID. Each leaf binds its first tailnet IP and checks the incoming node address and tag with WhoIs. Adding a tagged leaf connects it automatically. Renaming it preserves the connection. Removing its tag or withdrawing it from the map closes its connection. The plugin also reconciles every 30 seconds. If LocalAPI cannot confirm node status, it closes these connections and rediscovers peers on recovery. LocalAPI access suffices without a tailnet admin token or an address list. To change ports, set `discover.port`, the leaf’s `listen` and the tailnet grant to the same port.

Once connected, call services by their rutis service names. Give independently callable services from different leaves distinct names within a shared rutis scope. In rutis 0.23, `<link-row>:<plugin>` targets the first session on a row. Use a dedicated single-node row when loading a plugin on a particular leaf through that syntax.

For example, the integration test runs a service called `one` on one leaf and `two` on the other. On the host, each command returns `"hello"` from the corresponding leaf.

```sh
rutis-host call --config rutis.json one.echo '"hello"'
rutis-host call --config rutis.json two.echo '"hello"'
```

## Connect to a single node by name or IP

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

Add the following field to the discovery, dial or listener row's `config`. Give each row its own hostname and private absolute state directory. Supply an auth key through the plugin process's `TS_AUTHKEY` environment for initial enrollment. Restarts use the stored node identity. Unloading an embedded row shuts down its tsnet node. In system mode, unloading releases the plugin's listener and connections.

```json
{"tsnet":{"hostname":"rutis-bravo","stateDir":"/var/lib/rutis/tailscale/bravo"}}
```

Use the embedded node's actual tailnet name or IP in the other row's `dial` or `allowPeer`. Each end can choose its mode independently.

## Manage a row from another plugin

Declare `inject: ['rutis/loader']` in the management plugin. Run the following code from a management command or event handler after `apply` returns. It creates and starts the discovery row. Keep the returned row ID for subsequent operations.

```ts
const loader = ctx.use('rutis/loader')
const id = await loader.create({
  id: 'leaves', name: 'go:link-tailscale', config: { discover: { tag: 'tag:rutis-worker' } }
})
```

Use `loader.status()` to check the row's load state. Calling `loader.reload(id)` restarts discovery and reconnects visible leaves. Calling `loader.remove(id)` closes all sessions owned by the row and removes the row. To start it again, call `create` with the saved configuration. For static rows, edit and reload `rutis.json`.

A bind failure fails the listener row. Dialers retry in the background with exponential backoff from 0.5 to 30 seconds, resetting after a minute of connection. A running row may be retrying. Call a remote service to verify availability.

## Diagnostics and verification

| Observation | What to check |
| --- | --- |
| `tailscaled-unavailable` / `tailnet-not-ready` | Local daemon, login state and LocalAPI permissions. |
| `peer-not-found` / `node-key-expired` | Node name, visibility in the network map and node key expiry. |
| `tailnet-dial` / timeout | Target port, listener, device connectivity and access policy. A dropped ACL packet can surface as a timeout. |
| `peer-not-allowed` / `unknown-peer` | Configured listener peer and the source of the connection. |
| `path=direct` / `DERP` / `peer-relay` | The path observed when the stream was mounted. A pending observation shows `unknown`. Relaying can affect latency and throughput. Use `tailscale status` for the current path on system nodes. |

The shared Go TCP bridge uses a four-byte big-endian length prefix and a 16 MiB frame limit. Reconnection starts a fresh session without replay. The rutis 0.23 Go API exposes neither the peer's rutis Hello ID nor a session outbound queue limit. [TCP bridge](../../link/tcp/bridge.go) · [Tailscale CLI](https://tailscale.com/docs/reference/tailscale-cli)

Run these checks from the repository root. The integration test uses rutis-host 0.23.0 from PATH or `RUTIS_HOST`. A local test control server and DERP exercise pinned connections and reconnects, loader creation, reload and removal of a discovery row connecting two leaves, tag revocation and restoration, node renames and removal. Set `RUTIS_TAILSCALE_SYSTEM_TEST=1` to additionally exercise two host processes using the local enrolled system node.

```sh
go -C link/tailscale test -race ./...
go -C link/tailscale test -tags rutishost -run TestHosts ./...
```
