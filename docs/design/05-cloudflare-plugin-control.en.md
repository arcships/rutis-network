# Managing Cloudflare Tunnel from a Go plugin

[中文](05-cloudflare-plugin-control.md)

Start with **Access TCP and a named tunnel**. Give each link a local rutis service that other plugins can use to select a target, start or stop the link, and observe its state. A Go plugin supervises the official `cloudflared` process and controls one dedicated Go TCP plugin instance. The TCP instance handles mutual TLS (mTLS), framing, and sessions. The application injects one control service that stays available during an outage. [Cloudflare routing][routing] · [TCP plugin control API][tcp]

This proposal is dated 2026-11-26. The `go:link-cloudflared` plugin and its configuration fields below are proposed interfaces. The original Cloudflare draft targets rutis 0.21 / session protocol 4. This design depends on the Go implementation in PR #16 and targets rutis 0.23.0 / protocol 5 on both hosts. The two protocol versions are incompatible. [Cloudflare draft][draft] · [TCP implementation PR][tcp-pr] · [rutis migration guide][migration]

## Connecting business plugins on two hosts

Let A be a laptop running `consumer`, and B an office machine running `greeter`. The built-in `rutis-host/dial` and `rutis-host/listen` in rutis 0.23.0 already support this setup. Cloudflare carries one connection between hosts. Ordinary shared services are available in both directions. This example makes a call from A to B. [rutis nodes and services](https://github.com/arcships/rutis/blob/v0.23.0/docs/guide/nodes.en.md)

**B's `rutis.json`.** Place the Go plugin binary providing `greeter` in `plugins/go` beside the config file. Its `Apply` calls `row.Provide("greeter", &Greeter{})`, and its exported `Hello(name string) string` method returns a greeting.

```json
{
  "id": "office",
  "runtimes": { "go": { "dir": "plugins/go" } },
  "listen": [{ "name": "local", "address": "127.0.0.1:7443" }],
  "rows": [
    { "id": "laptop", "name": "rutis-host/listen", "config": { "listener": "local" } },
    { "id": "greeter", "name": "go:greeter" }
  ]
}
```

**A's `rutis.json`.** Install the Go binary providing `consumer` in this host's `plugins/go`. Port 17443 is A's local forwarder.

```json
{
  "id": "laptop",
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [
    { "id": "office", "name": "rutis-host/dial", "config": { "address": "ws://127.0.0.1:17443/rutis" } },
    { "id": "consumer", "name": "go:consumer" }
  ]
}
```

**Connect the hosts.** Give each `rutis-host` the same random link secret. Use `RUTIS_TOKEN_LAPTOP` on B and `RUTIS_TOKEN_OFFICE` on A. The suffix comes from the connection row ID on each host. Cloudflare uses separate credentials. [rutis node authentication](https://github.com/arcships/rutis/blob/v0.23.0/docs/guide/nodes.en.md)

1. In Cloudflare, configure a self-hosted Access application for `rutis.example.com` and a Service Auth policy admitting the designated service token. Add a TCP route for the same hostname to the named tunnel, targeting `tcp://127.0.0.1:7443`, with proxied DNS pointing to the tunnel. [Routing][routing] · [Access service tokens][access]
2. On B run `rutis-host run rutis.json`, and in a separate process run `cloudflared tunnel run --token-file /run/secrets/tunnel-token`. [Connector parameters][run]
3. On A pass the Access Client ID and Secret in `TUNNEL_SERVICE_TOKEN_ID` and `TUNNEL_SERVICE_TOKEN_SECRET` to `cloudflared access tcp --hostname rutis.example.com --url 127.0.0.1:17443`. Leave it running and start A's `rutis-host run rutis.json`. [Access client][cli]

**Complete Go example.** Save the following code as `main.go`. A single binary registers both plugins. Each host loads the one named by its configuration.

```go
package main

import (
    "context"
    "github.com/arcships/rutis/go/rutis"
)

type Greeter struct{}

func (*Greeter) Hello(name string) string { return "office: hello " + name }

var GreeterPlugin = rutis.Define(rutis.Plugin[rutis.NoConfig]{
    Name: "greeter",
    Apply: func(row *rutis.Ctx, _ rutis.NoConfig) error {
        row.Provide("greeter", &Greeter{})
        return nil
    },
})

type GreeterRef struct {
    Hello func(context.Context, string) (string, error)
}

type Consumer struct{ greeter GreeterRef }

func (c *Consumer) Greet(ctx context.Context, name string) (string, error) {
    return c.greeter.Hello(ctx, name)
}

var ConsumerPlugin = rutis.Define(rutis.Plugin[rutis.NoConfig]{
    Name:   "consumer",
    Inject: []string{"greeter"},
    Apply: func(row *rutis.Ctx, _ rutis.NoConfig) error {
        var g GreeterRef
        if err := row.Use("greeter", &g); err != nil {
            return err
        }
        row.Provide("consumer", &Consumer{greeter: g})
        return nil
    },
})

func main() { rutis.Serve(GreeterPlugin, ConsumerPlugin) }
```

Run these commands in a new directory containing the source. Install the resulting `business` binary in each host's configured directory, building for each target platform if they differ.

```sh
go mod init example.com/rutis-cloudflare-demo
go get github.com/arcships/rutis/go/rutis@v0.23.0
mkdir -p plugins/go
go build -o plugins/go/business .
```

On A run `rutis-host call --config rutis.json consumer.greet '"Rutis"'`. If B's method returns `"office: hello " + name`, A receives `"office: hello Rutis"`. A loopback test returned this value with two real rutis hosts, Go business plugins, and a real `cloudflared access tcp` process. A local test service simulated the Cloudflare edge and its forwarding to the origin. This example uses the built-in WebSocket link secret and Cloudflare terminates outer TLS. The sections below cover plugin supervision and end-to-end mTLS.

## Choosing the route

| Mode | Client and origin | When to use it |
| --- | --- | --- |
| Access TCP (first release) | Both hosts run `cloudflared`. The dialer forwards loopback TCP over WebSocket through Access. The origin connector connects outbound to Cloudflare and forwards to `tcp://127.0.0.1:7443`. | Unattended desktops and servers where the origin is behind NAT and the dialer cannot install a VPN. Reuses the TCP plugin in #16. |
| Native WSS (optional) | The client dials `wss://<hostname>`. Ingress points to an HTTP(S) origin supporting WebSocket upgrades, using the existing rutis WebSocket transport. Each binary message retains its boundary. | Browsers and clients unable to run a helper process. A browser signs in to Access on the same hostname first and sends its applicable cookie on upgrade. Machine credentials stay off the frontend. A native Go client can send service token headers on upgrade. |
| WARP / Client-to-Tunnel | Enroll the device in Cloudflare One Client and configure a private route, Split Tunnels, and access policy. The Go client dials a routable private service address. A loopback-only listener needs a reachable ingress. | Evaluate first for managed devices that depend on long-lived connections. Edge restarts can interrupt both WebSocket options. |

The first release uses a Service Auth policy scoped to a specific machine token. Outer WebSocket TLS terminates at Cloudflare. Inner mTLS reaches the TCP listener, protecting both loopback endpoints as well. The TCP plugins use the #16 four-byte big-endian length prefix and 16 MiB frame limit. Failed admission creates zero rutis sessions. The remote plugin ID comes from the rutis Hello. [Access service tokens][access] · [Cloudflare WebSockets][ws] · [Browser WebSocket interface][browser] · [Split Tunnels][warp]

## Host configuration

The proposed dialer configuration assumes the two Go plugin binaries in `plugins/go` and an installed `cloudflared`. Each deployment profile binds the public hostname, the name on the origin certificate, and the credential references together. Runtime callers can select a declared profile, so a target change cannot send an existing credential to a caller-supplied hostname.

```json
{
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [
    {
      "id": "wire",
      "name": "go:link-tcp-dial",
      "isolate": { "office-tcp": "office-internal" },
      "config": {
        "controlService": "office-tcp", "autoStart": false, "reconnect": true,
        "ca": "/etc/rutis/pki/ca.pem", "cert": "/etc/rutis/pki/client.pem", "key": "/etc/rutis/pki/client.key"
      }
    },
    {
      "id": "office-tunnel",
      "name": "go:link-cloudflared",
      "inject": ["office-tcp"],
      "isolate": { "office-tcp": "office-internal", "office-link": "office-management" },
      "config": {
        "role": "dial", "tcpService": "office-tcp", "controlService": "office-link",
        "autoStart": false, "forwardPort": 17443, "profile": "lab",
        "profiles": {
          "lab": {
            "hostname": "rutis.example.com", "servername": "origin.internal",
            "clientIdFile": "/run/secrets/access-id", "clientSecretFile": "/run/secrets/access-secret"
          }
        }
      }
    }
  ]
}
```

On the origin host, use the same Go runtime and the following two rows. Port 7443 must match the remote ingress configuration. The dialer verifies `origin.internal` on the server certificate. The listener verifies client certificates against its configured CA. [TCP certificate and listener configuration][tcp-guide]

```json
[
  {
    "id": "wire", "name": "go:link-tcp-listen",
    "isolate": { "origin-tcp": "origin-internal" },
    "config": {
      "controlService": "origin-tcp", "autoStart": false,
      "ca": "/etc/rutis/pki/ca.pem", "cert": "/etc/rutis/pki/server.pem", "key": "/etc/rutis/pki/server.key"
    }
  },
  {
    "id": "office-tunnel", "name": "go:link-cloudflared",
    "inject": ["origin-tcp"],
    "isolate": { "origin-tcp": "origin-internal", "office-origin": "origin-management" },
    "config": {
      "role": "connector", "tcpService": "origin-tcp", "controlService": "office-origin",
      "autoStart": true, "originPort": 7443, "tunnelTokenFile": "/run/secrets/tunnel-token"
    }
  }
]
```

A local caller declares `"inject": ["office-link"]` and `"isolate": {"office-link":"office-management"}` on its own row. Go binds the methods with `ctx.Use("office-link", &control)`. Other languages use their SDKs. Only the tunnel plugin consumes the underlying TCP control service. Isolation matters because rutis exports ordinary root-scoped services to connected peers. Use `wire:<plugin>` to run a plugin at the far end. Sessions belong to the TCP row that calls `rutis/sessions.open`. For dynamic mounting, a local plugin can inject `rutis/loader` and create rows in a preconfigured isolated group. [Host export rules][exports] · [Dynamic mounting example][tcp]

```go
func startLab(callCtx context.Context, row *rutis.Ctx) error {
    var link struct {
        Configure func(context.Context, map[string]any) error
        Start     func(context.Context) error
    }
    if err := row.Use("office-link", &link); err != nil {
        return err
    }
    if err := link.Configure(callCtx, map[string]any{"profile": "lab"}); err != nil {
        return err
    }
    return link.Start(callCtx)
}
```

## Plugin control contract

| Method | Caller-visible behavior |
| --- | --- |
| `configure({profile})` | On the dialer, validate before switching. An identical patch is idempotent and an invalid one preserves the current connection. Switching a running link closes the old connection first. A stopped link stores the selection in memory. Reload restores the deployment profile. |
| `start()` | Idempotently accepts a start request. The dialer runs `cloudflared access tcp --hostname <hostname> --url 127.0.0.1:<forwardPort>`, configures the TCP row with loopback, the forward port, and the origin certificate name, then starts the row. The origin starts the configured TCP listener, waits for the bind, and runs `cloudflared tunnel run --token-file <path>`. |
| `stop()` | Idempotently cancels local retries, stops the dedicated TCP instance and its sessions, terminates the owned child, and waits for its exit. The row and remote resources persist. |
| `status()`, `waitForChange(cursor)` | Read the latest snapshot. A stale cursor returns immediately, a current one waits for a change, and callers can cancel the wait. Fields include `role`, `desired`, dialer `profile`, `process`, `tcpPhase`, `lastError`, `retryAt`, and an opaque `cursor`. Connector `edgeReady` comes from the loopback metrics endpoint's `/ready` and is null when unknown. |

The Go supervisor starts the binary directly with `os/exec`, serializes `configure/start/stop`, and registers one cleanup per row. It backs off and restarts a child on exit. A stream loss while the child is alive belongs to the TCP plugin's reconnect loop. A new session never replays old calls. A missing binary or bind collision ends the attempt with the failing stage recorded. Reserve the forwarding port for the instance. On a shared host, verify the listener socket belongs to the owned child before starting the TCP row. [cloudflared forwarding implementation][carrier] · [TCP retry behavior][tcp]

Read the dialer's credential files at child start and set `TUNNEL_SERVICE_TOKEN_ID` and `TUNNEL_SERVICE_TOKEN_SECRET` only in that child's environment. Give the connector its tunnel token file and an explicit environment that excludes the overriding `TUNNEL_TOKEN`. Restart the relevant child to pick up rotated credentials. Return sanitized errors in status, RPCs, and logs. Use normal logging because the reviewed debug implementation dumps request headers. [CLI and headers][cli] · [Debug logging implementation][debug] · [Token file parameter][run]

**What each signal proves.** A loopback connection shows that a local process is listening. Successful `/ready` shows an active connector-to-edge connection. The TCP plugin marks itself `connected` after local mTLS verification and handoff to the session service. Remote service registration or a successful call establishes application readiness. Access logs can confirm a policy denial. A local EOF alone warrants a transport failure with an unknown cause. The 0.23 Go session API lacks the greeted peer ID and an outbound queue limit. Use a Rust bridge with handshake-based replacement for same-ID takeover, or with an explicit outbound limit for deployments requiring a hard queue bound. [Connector readiness implementation][ready] · [rutis session API and limits][tcp-guide]

## Cloud resource ownership

Use preconfigured resources for the first release. Normal operation requires only the respective runtime credentials on each host. For the exact public hostname in a Cloudflare-managed zone, create a self-hosted Access application and attach a Service Auth policy for the designated service token. Create a remotely managed named tunnel with `config_src: "cloudflare"`, configure the following ingress, then publish a proxied CNAME from `rutis.example.com` to `<tunnel-id>.cfargotunnel.com`. This order puts the policy in place before publication. [Tunnel API setup][create] · [Access policies and tokens][access]

```json
{
  "config": {
    "ingress": [
      { "hostname": "rutis.example.com", "service": "tcp://127.0.0.1:7443" },
      { "service": "http_status:404" }
    ]
  }
}
```

If provisioning also needs a plugin interface, deploy a **separate Go plugin on the management host**. Configure its `accountId`, `zoneId`, dedicated tunnel ID or creation name, hostname, origin, existing Access policy ID, and management API token file. Expose `plan()` to return the diff and `apply(planId)` to reread the resources and apply a current plan. The Go SDK covers tunnels, configuration, Access, and DNS. Scope the token to tunnel and Access application writes in the account and DNS writes in the target zone. Reuse an existing policy and service token. The reviewed `cloudflare-go/v7@v7.15.0` requires Go 1.26. A separate module keeps the runtime plugins on the Go 1.24 baseline from #16. [Cloudflare API][api] · [Access application API][apps] · [Go SDK requirement][sdk]

Ingress updates PUT the complete configuration. The management plugin should write only a dedicated tunnel, confirm Access, ingress, and DNS in order, and record resource IDs and configuration versions. After partial success, reread and reuse the resources already created. Report a conflict on unmanaged name collisions or configuration drift. To roll back, withdraw the newly created DNS entry, restore the ingress changed by this operation, then handle dedicated resources it created. Dashboard- or Terraform-owned resources stay with their original writer. [Tunnel configuration API][api]

## Evidence and acceptance

- **Verified locally** on macOS with `cloudflared 2026.11.1`. An `access tcp` forwarder pointed to a loopback test peer that closed on TLS ClientHello. Two consecutive local TCP connects succeeded and then read EOF. The same child stayed alive throughout, and terminating it released the forward port. This verifies the distinction between process and stream health.
- **Implementation acceptance** should run two real rutis hosts and a local process fixture. Cover a successful service call, zero sessions after Access denial or mTLS failure, bind collision, rotation, concurrent start/stop, and process cleanup on reload. Confirm the far end cannot invoke management services and that snapshots and logs omit fixture secrets.
- **Live-account validation remains open.** On a test hostname, open new connections with valid and revoked credentials and check Access logs and a remote service call. Interrupt and restore the connector, verify the old session disappears and a fresh call succeeds, then stop the instance and confirm cloud resources persist. No public end-to-end session was exercised in this study.

[routing]: https://developers.cloudflare.com/tunnel/concepts/routing/
[draft]: https://github.com/arcships/rutis-network/pull/15
[tcp-pr]: https://github.com/arcships/rutis-network/pull/16
[access]: https://developers.cloudflare.com/cloudflare-one/access-controls/service-credentials/service-tokens/
[ws]: https://developers.cloudflare.com/network/websockets/
[browser]: https://websockets.spec.whatwg.org/#the-websocket-interface
[warp]: https://developers.cloudflare.com/cloudflare-one/team-and-resources/devices/cloudflare-one-client/configure/route-traffic/split-tunnels/
[exports]: https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/session/plugin/services.rs
[carrier]: https://github.com/cloudflare/cloudflared/blob/2026.11.1/carrier/carrier.go
[cli]: https://github.com/cloudflare/cloudflared/blob/2026.11.1/cmd/cloudflared/access/carrier.go
[debug]: https://github.com/cloudflare/cloudflared/blob/2026.11.1/carrier/websocket.go
[run]: https://developers.cloudflare.com/tunnel/reference/run-parameters/#token-file
[ready]: https://github.com/cloudflare/cloudflared/blob/2026.11.1/metrics/readiness.go
[create]: https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/create-remote-tunnel-api/
[api]: https://developers.cloudflare.com/api/go/resources/zero_trust/subresources/tunnels/subresources/cloudflared/
[apps]: https://developers.cloudflare.com/api/resources/zero_trust/subresources/access/subresources/applications/methods/create/
[sdk]: https://github.com/cloudflare/cloudflare-go/blob/v7.15.0/go.mod
[tcp]: https://github.com/arcships/rutis-network/blob/eaa4c1ba1ffc4cd961d863f23a7bb4b5aaeae396/docs/design/06-link-tcp-integration.en.md
[tcp-guide]: https://github.com/arcships/rutis-network/blob/eaa4c1ba1ffc4cd961d863f23a7bb4b5aaeae396/docs/guide/link-plugins.en.md
[migration]: https://github.com/arcships/rutis/blob/v0.23.0/docs/migration-0.23.en.md
