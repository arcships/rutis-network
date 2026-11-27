# LAN discovery with Go and DNS-SD/mDNS

[中文](README.zh-CN.md)

Use `go:discover-mdns` to find service instances on one multicast-capable interface or advertise an existing local ingress. A browsing row provides a named read-only rutis service. A group policy can use these observations to select neighbors and give a link plugin a dial target. The link owns admission and session establishment. [Network group design](../../docs/research/network-groups.en.md)

## Connect over mTLS using a discovered offer

The listener advertises its bound port when it is ready. The dialer is configured with the offer ID, permitted destination ranges and expected certificate name. Start with the two complete rutis-host manifests. [Listener configuration](example/listener.json) · [Dialer configuration](example/dialer.json)

```mermaid
flowchart LR
  listen["TCP listener"] -->|"status / waitForChange"| source["tcp-offer-source"]
  source -->|"Public descriptions"| pub["discover-mdns publisher"]
  pub -->|"DNS-SD (larger params via HTTP)"| browse["discover-mdns browser"]
  browse -->|"description"| select["tcp-offer-dial"]
  select -->|"configure / start"| dial["TCP dialer"]
  dial -->|"mTLS session"| listen
```

| Row | Configuration |
| --- | --- |
| TCP row on each host | Set its local `ca`, `cert`, `key` and a dedicated `controlService`. The listener uses `port: 0` to allocate a port. The dialer uses `autoStart: false` to wait for discovery. |
| Publishing `go:tcp-offer-source` | Set `listener` to the TCP control service and `service` to the name of the offer source. Choose a stable `id` and optionally a `serverName` hint. |
| Publishing `go:discover-mdns` | Choose an `interface` and point `offers.source` and the row's `inject` at the source. Stopping the listener, removing the offer or unloading the source withdraws the advertisement. |
| Browsing `go:discover-mdns` | Set `offers.accept: {"rutis.tcp":[1]}` and name its local observation `service`. To retrieve larger descriptions, set `offers.allowCIDRs` to the permitted HTTP address ranges. |
| Receiving `go:tcp-offer-dial` | Inject the observation service and a dedicated dial control. Configure the `id`, locally expected `serverName` and `allowCIDRs` for dial targets. One matching offer with an allowed target and compatible name hint starts the dialer. Withdrawal, expiry or ambiguity stops it. |

For example, the observed `description` can be `{"id":"office","kind":"rutis.tcp","version":1,"params":{"host":"127.0.0.1","port":51827,"serverName":"office.example"}}`. The TCP dialer uses `params.port`. The candidate's outer `port` belongs to the HTTP description service. This adapter accepts IP literals and requires a loopback observation for a loopback target. Each connector reads its CA, certificate and key from local configuration.

Build both plugins from the repository root and put the binaries in each manifest's Go runtime directory. The manifests use macOS `lo0` for two rutis hosts on one machine. Set the certificate paths for each host and issue the server certificate for the expected DNS name. Run each host with `rutis-host run listener.json` or `rutis-host run dialer.json` from its own project directory. The current Go TCP listener binds loopback. For a cross-machine deployment with a forwarder, the plugin owning the forwarder should publish its reachable ingress when ready.

```sh
mkdir -p plugins/go
(cd discovery/mdns && go build -o ../../plugins/go/discover-mdns ./cmd/discover-mdns)
(cd link/tcp && go build -o ../../plugins/go/link-tcp ./cmd/link-tcp)
```

To integrate another protocol, implement a local source with `snapshot()` and `waitForChange(cursor)`. Both return `{"cursor":"…","offers":[…]}` as a full replacement. Return `offers: []` to withdraw all offers. If `offers` is missing or null, the publisher keeps the last accepted set and retries. Each row consumes one source with up to 128 offers, each carrying a public JSON object of at most 64 KiB in `params`. New protocols provide their own mappings on each side. mDNS carries the kind, version and parameters and scopes browsing and HTTP requests to the configured interface. The receiver checks the parameter digest and the connector authenticates the peer using local trust policy. [Connection description design](../../docs/design/connection-descriptions.en.md)

The publisher serves long descriptions on one port across the selected interface’s available address families. To bind a single address, set publishing `offers.listenIP` to an address on that interface. The browser retries other permitted addresses within a shared three-second deadline. Adding an address keeps an already verified offer available while its serving address remains live.

Run the end-to-end test from the repository root. It issues separate server and client identities and verifies discovery, a remote RPC, rejection of a client without a certificate and withdrawal on source unload.

```sh
npm ci --prefix link/tcp/node
MDNS_TEST_INTERFACE=lo0 node --test discovery/mdns/test/hosts.test.mjs
```

## Ordinary DNS-SD endpoints

**One advertisement describes one DNS-SD service instance, an ingress that a peer on this LAN can try to reach.** For example, the instance named `office` offers `_rutis._tcp` at TCP port `7443` on `edge-b.local.`. The service type tells a client which protocol to use after connecting. The instance name distinguishes several ingresses of the same type. A direct listener or a transparent TCP forwarder can provide this ingress. Both deployments can publish the same advertisement when they expose the same protocol at the same address.

```mermaid
flowchart LR
    config["Configured ingress port"] --> ad["Service instance office<br/>_rutis._tcp · edge-b.local.:7443"]
    ad -->|"Advertise on selected interface"| peer["Browser on the LAN<br/>resolves addresses and port"]
```

| Browser question | DNS records in this example |
| --- | --- |
| Which instances offer this service? | PTR points from `_rutis._tcp.local.` to `office._rutis._tcp.local.`. |
| Where do I connect to the selected instance? | SRV supplies `edge-b.local.` and port `7443`. A and AAAA supply that host's IPv4 and IPv6 addresses. |

The publishing row configures the type, instance, port and outgoing interface. Bonjour or Avahi supplies the local target host name and addresses. DNS-SD also carries service attributes in TXT records. An ordinary endpoint publishes an empty set of attributes. The browser adds its receiving interface, its own row ID and an expiry derived from record TTLs to the candidate snapshot. The link verifies peer identity during admission. [DNS-SD instance resolution](https://www.rfc-editor.org/rfc/rfc6763.html#section-5)


## Run two rows on a LAN

Build from the repository root with Go 1.26 and use rutis-host 0.23.0. Install the binary in the configured Go runtime directory on both hosts.

```sh
mkdir -p plugins/go
(cd discovery/mdns && go build -o ../../plugins/go/discover-mdns ./cmd/discover-mdns)
```

On A, save this as `rutis.json`. Replace `eth0` with A's LAN interface. It must be up and allow mDNS multicast on UDP 5353. The domain defaults to `local.`.

```json
{
  "id": "node-a",
  "runtimes": {
    "go": {
      "dir": "plugins/go"
    }
  },
  "rows": [
    {
      "id": "browse-lan",
      "name": "go:discover-mdns",
      "isolate": {
        "lan-discovery": "management"
      },
      "config": {
        "interface": "eth0",
        "domain": "local.",
        "browse": "_rutis._tcp",
        "service": "lan-discovery"
      }
    }
  ]
}
```

On B, use the same Go runtime directory and add this row to its `rows`. Select B's LAN interface and the port of an ingress already reachable there. The publisher uses the system responder's addresses for the local host. Start each host with `rutis-host run rutis.json`.

```json
{
  "id": "announce-ingress",
  "name": "go:discover-mdns",
  "config": {
    "interface": "eth0",
    "publish": {
      "type": "_rutis._tcp",
      "instance": "node-b",
      "port": 7443
    }
  }
}
```

Publishing supports macOS and Linux on amd64 and arm64. macOS supplies Bonjour. On Linux, run Avahi and install its DNS-SD compatibility library (`libavahi-compat-libdnssd1` on Debian/Ubuntu). Only publishing loads that library. The row waits for registration and fails if the responder assigns a different instance name. Unloading it withdraws the registration. Reload the row after a responder failure.

Each row browses one type or publishes one instance, and can do both. Use another row and service name for another interface. Types use the form `_name._tcp` or `_name._udp`. The instance is a single UTF-8 label of up to 63 bytes and may include spaces or dots. The host running the ingress determines when to enable or unload its publishing row.

## Consume the candidate service

Give a consuming row `"inject": ["lan-discovery"]` and `"isolate": {"lan-discovery": "management"}` to match A. A Go consumer can import `github.com/arcships/rutis-network/discovery/mdns` and use the following in its `Apply` function. Implement `updateNeighbors` with the consumer's own selection policy.

```go
var discovery struct {
    Snapshot      func(context.Context) (mdns.Snapshot, error)
    WaitForChange func(context.Context, string) (mdns.Snapshot, error)
}
if err := row.Use("lan-discovery", &discovery); err != nil {
    return err
}
row.Go(func(ctx context.Context) error {
    state, err := discovery.Snapshot(ctx)
    for err == nil {
        // updateNeighbors replaces the consumer's local view.
        updateNeighbors(state.Candidates)
        state, err = discovery.WaitForChange(ctx, state.Cursor)
    }
    return err
})
return nil
```

Both methods return the latest full view. An empty list is meaningful. An unknown or previous-run cursor returns immediately. Waiting on the current cursor ends on an update or cancellation. A TTL refresh can advance the cursor for the same instance.

```json
{
  "cursor": "sample/2",
  "candidates": [{
    "source": "browse-lan", "interface": "eth0", "domain": "local.",
    "serviceType": "_rutis._tcp", "instance": "node-b", "transport": "tcp",
    "addresses": ["192.0.2.8", "fe80::8%eth0"], "port": 7443,
    "expiresAt": "2026-11-27T08:02:00Z"
  }]
}
```

Key candidates by `source` (the browsing row ID), `interface`, `domain`, `serviceType` and `instance`. `expiresAt` is the earliest expiry among the PTR, SRV and addresses in this view. The next snapshot can retain an instance with fewer addresses. TCP/UDP, port and addresses come from DNS. A caller should check scope, expiry and its own allowed address ranges before handing a target to a link. Supply the expected peer identity separately from local policy.

## Verify

Run in `discovery/mdns`. The live test needs `rutis-host` 0.23.0 on `PATH` or `RUTIS_HOST` set to its executable. Select an interface on which the system responder advertises services. `lo0` works on macOS. On Linux, select an interface enabled in Avahi.

```sh
go test -race ./...
go vet ./...
MDNS_TEST_INTERFACE=lo0 go test -race -count=1 -run TestLive ./...
```

The live test loads separate browsing and publishing rows, waits for the resolved candidate and unloads the publisher to verify withdrawal. Deterministic tests cover split DNS replies, individual TTL expiry, cache flushes, goodbyes, IPv6 zones and cursor cancellation. [Multicast DNS](https://www.rfc-editor.org/rfc/rfc6762) · [DNS-Based Service Discovery](https://www.rfc-editor.org/rfc/rfc6763)
