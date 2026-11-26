# Managing Cloudflare Tunnel from a Go plugin

[中文](05-cloudflare-plugin-control.md)

`go:link-cloudflared` supervises a Cloudflare Access TCP forwarder or an origin connector from a rutis row. Install rutis 0.23.0 (protocol 5) and `cloudflared` on both hosts (token files require 2025.4.0 or later). Configure a named tunnel in Cloudflare. Use built-in rutis WebSocket rows for the initial connection, or the TCP control service from PR #16 for end-to-end mTLS. [Cloudflare routing][routing] · [Connector parameters][run] · [TCP implementation PR][tcp-pr]

## Connecting business plugins on two hosts

Build from the repository root with Go 1.24 or later. Install both binaries for each host's platform in `plugins/go` beside its `rutis.json`. Office host B provides `greeter.hello`. On laptop A, `consumer` injects `greeter` and calls its method. [Complete Go business plugins](../../link/cloudflared/example/business/main.go) · [rutis nodes and services](https://github.com/arcships/rutis/blob/v0.23.0/docs/guide/nodes.en.md)

```sh
mkdir -p plugins/go
go -C link/cloudflared build -o ../../plugins/go/link-cloudflared ./cmd/link-cloudflared
go -C link/cloudflared build -o ../../plugins/go/business ./example/business
```

**Office host B's `rutis.json`**

```json
{
  "id": "office",
  "runtimes": { "go": { "dir": "plugins/go" } },
  "listen": [{ "name": "local", "address": "127.0.0.1:7443" }],
  "rows": [
    { "id": "laptop", "name": "rutis-host/listen", "config": { "listener": "local" } },
    { "id": "greeter", "name": "go:greeter" },
    {
      "id": "origin", "name": "go:link-cloudflared",
      "config": { "role": "connector", "tunnelTokenFile": "/run/secrets/tunnel-token", "metricsPort": 20411 }
    }
  ]
}
```

**Laptop A's `rutis.json`**

```json
{
  "id": "laptop",
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [
    {
      "id": "access", "name": "go:link-cloudflared",
      "isolate": { "office-link": "management" },
      "config": {
        "role": "dial", "forwardPort": 17443, "profile": "office", "controlService": "office-link",
        "profiles": { "office": {
          "hostname": "rutis.example.com", "clientIdFile": "/run/secrets/access-id",
          "clientSecretFile": "/run/secrets/access-secret"
        } }
      }
    },
    { "id": "office", "name": "rutis-host/dial", "config": { "address": "ws://127.0.0.1:17443/rutis" } },
    { "id": "consumer", "name": "go:consumer" }
  ]
}
```

1. Protect `rutis.example.com` with a self-hosted Access application and a Service Auth policy admitting the designated token. Configure a TCP route on the named tunnel to `tcp://127.0.0.1:7443` and its proxied DNS record. Install the tunnel token file on B and the Access Client ID and Secret files on A, readable by the respective runtime accounts. [Tunnel setup][create] · [Access service tokens][access]
2. Set B's `RUTIS_TOKEN_LAPTOP` and A's `RUTIS_TOKEN_OFFICE` to the same random link secret. Each suffix identifies that host's connection row. Run `rutis-host run rutis.json` on each host, starting B first. The Go plugins launch their own `cloudflared` processes.
3. On A run `rutis-host call --config rutis.json consumer.greet '"Rutis"'`. The result `"office: hello Rutis"` confirms A's business plugin called B. Ordinary services are shared in both directions. Scope local management services with `isolate`.

Cloudflare terminates outer TLS in this setup, with the rutis link secret admitting the inner WebSocket connection. The Go forwarder binds IPv4 loopback and allows one active stream. Use a trusted host or dedicated network namespace where other local users could occupy that stream. Stopping the Cloudflare row closes its transport. The independent `rutis-host/dial` row continues its own reconnect policy.

## Choosing the route

| Mode | Client and origin | When to use it |
| --- | --- | --- |
| Access TCP (first release) | Both hosts run `cloudflared`. The dialer forwards loopback TCP over WebSocket through Access. The origin connector connects outbound to Cloudflare and forwards to `tcp://127.0.0.1:7443`. | Unattended desktops and servers where the origin is behind NAT and the dialer cannot install a VPN. Reuses the TCP plugin in #16. |
| Native WSS (optional) | The client dials `wss://<hostname>`. Ingress points to an HTTP(S) origin supporting WebSocket upgrades, using the existing rutis WebSocket transport. Each binary message retains its boundary. | Browsers and clients unable to run a helper process. A browser signs in to Access on the same hostname first and sends its applicable cookie on upgrade. Machine credentials stay off the frontend. A native Go client can send service token headers on upgrade. |
| WARP / Client-to-Tunnel | Enroll the device in Cloudflare One Client and configure a private route, Split Tunnels, and access policy. The Go client dials a routable private service address. A loopback-only listener needs a reachable ingress. | Evaluate first for managed devices that depend on long-lived connections. Edge restarts can interrupt both WebSocket options. |

The first release uses a Service Auth policy scoped to a specific machine token. Outer WebSocket TLS terminates at Cloudflare. With the optional TCP plugin, inner mTLS reaches the TCP listener and protects both loopback endpoints. The TCP plugins use the #16 four-byte big-endian length prefix and 16 MiB frame limit. Failed admission creates zero rutis sessions. The remote plugin ID comes from the rutis Hello. [Access service tokens][access] · [Cloudflare WebSockets][ws] · [Browser WebSocket interface][browser] · [Split Tunnels][warp]

## Host configuration

For the mTLS variant, also install the Go TCP plugin binary from PR #16 in `plugins/go`. The following rows configure a dedicated TCP instance on each host. Each deployment profile binds the public hostname, the name on the origin certificate, and the credential references together. Runtime callers can select a declared profile, so a target change cannot send an existing credential to a caller-supplied hostname.

```json
{
  "id": "laptop",
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

On the origin host, set `"id": "office"` and use the same Go runtime with the following two rows. Port 7443 must match the remote ingress configuration. The dialer verifies `origin.internal` on the server certificate. The listener verifies client certificates against its configured CA. [TCP certificate and listener configuration][tcp-guide]

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
| `start()` | Idempotently accepts a start request. The Go dialer binds loopback itself. Each accepted stream runs `cloudflared access tcp --hostname <hostname>` in stdin/stdout mode. If configured, the dedicated TCP row dials that listener and verifies the origin certificate name. The origin starts the configured TCP listener, waits for the bind, and runs `cloudflared tunnel run --token-file <path>`. |
| `stop()` | Idempotently cancels local retries, stops the dedicated TCP instance and its sessions, terminates the owned child, and waits for its exit. The row and remote resources persist. |
| `status()`, `waitForChange(cursor)` | Read the latest snapshot. A stale cursor returns immediately, a current one waits for a change, and callers can cancel the wait. Fields include `role`, `desired`, dialer `profile`, `process`, `tcpPhase`, `lastError`, `retryAt`, and an opaque `cursor`. Set `metricsPort` to a dedicated loopback port for connector `/ready` probes. `edgeReady` is null when unconfigured or unknown. `tcpPhase`, `tcpError` and `tcpRetryAt` come from the optional TCP instance. |

The connector restarts its process after an exit with a 0.5–30 s backoff, reset after 60 s of uptime. On the dialer the session adapter owns retries, and each accepted connection gets a fresh child and fresh credentials. The Go listener releases the socket and both pipes on either EOF or cancellation. A missing binary or occupied forwarding port reports `binary-unavailable` or `address-in-use`. Path overrides belong in the static `binary` field. For a managed TCP row, port 0 selects a free port and publishes it as `boundPort`. For the built-in dialer configure the same fixed port on both rows. [cloudflared forwarding implementation][carrier] · [TCP retry behavior][tcp]

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
- **Automated checks** exercise binary streams, bind conflicts, credential rotation, profile changes, connector retries and concurrent controls. The two-host test uses a real Access TCP client, a simulated edge and an origin connector fixture. It rejects an invalid credential, returns a Go service result after rotation, verifies management isolation, and unloads and remounts just the dialer row. Both built-in WebSocket and #16 mTLS configurations are covered. [Host tests](../../link/cloudflared/hosts_test.go)
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
