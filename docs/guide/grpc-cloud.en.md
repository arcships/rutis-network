# Connect two outbound-only hosts over gRPC

Both hosts mount `go:link-grpc`, register with a directory and dial the same regional relay. Neither host needs an inbound port. The relay pairs two bidirectional HTTP/2 streams and forwards messages in order. After admission the hosts call `rutis/sessions.open` and exchange their own rutis `Hello`. A device with a reachable Go service can also connect directly to a plugin on that service's existing listener. [Direct integration](link-plugins.en.md#a-device-plugin-and-a-plugin-embedded-in-a-go-cloud-service) · [Cloud wire protocol](../../link/grpc/wire/cloud.proto)

## Run the example

The deployment has one directory, east and west relays, an edge host and a worker host. The edge row `wire:go:worker-jobs` mounts the installed Go plugin on the worker. Calling `device.submit` on the edge receives progress through a reverse callback.

1. Build the programs below from the repository root. Each host gets a copy of `link-grpc` in its Go plugin directory.
2. The manifests use loopback addresses on one machine and a trusted CA. Across machines, advertise relay addresses reachable from both hosts and configure the directory address on both hosts and agents. Bind each server to its deployment address.
3. Supply the PEM files referenced by the manifests. The directory and relays need serverAuth certificates with DNS SANs `directory.test`, `east.test` and `west.test`. Give the regional agents separate clientAuth certificates for `east-agent.test` and `west-agent.test`. Issue clientAuth certificates for `edge.test`, `worker.test` and `operator.test`. Provide a writable persistent directory for the directory's `stateFile`. [Deployment manifests](../../link/grpc/example/relay)

```sh
cd link/grpc
go build -o /tmp/grpc-cloud ./cmd/grpc-cloud
go build -o example/relay/edge/plugins/go/link-grpc ./cmd/link-grpc
go build -o example/relay/worker/plugins/go/link-grpc ./cmd/link-grpc
go build -o example/relay/edge/plugins/go/device ./example/device
go build -o example/relay/worker/plugins/go/cloud-service ./example/cloud
```

Start these long-lived commands in separate terminals. The directory waits 12 seconds at startup for any previously granted regional leases to expire.

```sh
/tmp/grpc-cloud example/relay/control.json
/tmp/grpc-cloud example/relay/east.json
/tmp/grpc-cloud example/relay/west.json
rutis-host run example/relay/worker/rutis.json
rutis-host run example/relay/edge/rutis.json
```

On the edge run `rutis-host call --config example/relay/edge/rutis.json device.submit '"build"'`. A fresh worker returns `{"id":"job-1","progress":"job-1 queued"}`. For a local run with temporary ports and certificates, run `npm ci --prefix link/tcp/node` and `make integration-grpc` from the repository root. The suite also partitions a region from the directory. [Deployment test](../../link/grpc/deployment_test.go) · [Host failover test](../../link/grpc/relay_hosts_test.go)

To stop the whole deployment, stop both relays, wait for the edge to withdraw the remote service, then stop the hosts and directory. This preserves the manifests. On rutis-host 0.23.0, signaling a host with an active remote business row can occasionally reach the 10 s shutdown deadline during row teardown. To retire a single remote business row while the link is available, await `rutis/loader.remove`. This updates the editable manifest.

## Configure an integrating host

The row takes JSON from any host language. Top-level `address`, `serverName` and `caFile` identify the directory. Set `cloud.pair` to the agreed pair and `cloud.target` to the other member. The remote row uses the same pair and the reciprocal target. Credentials identify the member and tenant. A pair name can be reused across tenants. [Edge manifest](../../link/grpc/example/relay/edge/rutis.json) · [Worker manifest](../../link/grpc/example/relay/worker/rutis.json)

| Setting | Behavior |
| --- | --- |
| `certFile`, `keyFile` | Mutual TLS. The directory looks up the exact DNS SAN of the verified leaf certificate. |
| `cloud.bearerFile` | File containing an API key or an OAuth-issued RS256 JWT. A bearer-only client needs the server CA. The client rereads the token on registration and reconnect. |
| `cloud.region` | Optional preferred region. On initial placement without a preference the client probes eligible healthy regions concurrently and uses the first successful TLS/gRPC round trip. Probes have a shared 1.5 s budget. The ordered allowlist supplies the fallback. |
| `cloud.capabilities`, `cloud.load` | Optional capability names and a load from 0 to 100. An admitted client refreshes its registration every 10 s. A Go embedder can call `Client.UpdateRegistration(capabilities, load)` to change the next report. |

Go embedders can call `MountClient` and use its `Start`, `Stop` and `Status` methods. Status carries the phase, chosen region, generation, last gRPC code, structured reason and next retry time. The admitted phase indicates transport admission. A service call checks application readiness. An incompatible protocol suspends retries until restart. When a new instance takes over a member, the old instance enters `replaced` until an explicit `Start` or process restart. Its peer reconnects immediately on `PEER_REPLACED`. Admission denials retry after 30 s, other disconnects back off from 0.5 to 30 s. [Client implementation](../../link/grpc/client.go)

## Directory, admission and inspection

- Configure each pair with a tenant, two reciprocal members and 1–16 permitted data regions. `maxPairs[tenant]` limits simultaneous reservations, including a member waiting for its peer. Idle reservations expire within 42 s of the last allocation or activity report, or when the region loses its lease. The directory reserves two slots against regional `maxStreams` when it issues the first ticket. `Inspect` reports the actual stream count and the limit.
- For an API key, set the member's `keySHA256` to the hex SHA-256 digest of a high-entropy random key. JWT admission uses a member's `subject` and the directory's `jwt` object with `issuer`, `audience`, `scope` and `publicKeyFile`. The directory verifies RS256, expiration, issuer, audience, subject and scope. Only the directory receives the original key or JWT. Restart the relevant server to apply configuration changes. The stock client ends its stream after an explicit registration denial. Draining the affected relay forcibly closes any already-admitted streams.
- The directory issues a 30 s single-use ticket bound to the current regional lease, pair, target and generation. Tickets and reservations from an expired lease stay invalid even if the same relay process reacquires it. An endpoint that presented a client certificate must present the same leaf at the relay. The relay redeems each ticket over its own mTLS connection. It acknowledges admission when both streams arrive. A lone member receives `Unavailable` and `PAIR_TIMEOUT` after the default 20 s wait. It immediately requests a fresh ticket to continue waiting for its peer. Its `google.rpc.ErrorInfo` includes the pair, target and generation.
- Call `rutis.link.v1.Cloud/Inspect` using the client certificate named by `adminCert`. It returns registrations with last-seen time and freshness, regional lease availability, pairs, streams, forwarded messages, payload bytes and error counts by gRPC code. Traffic counters cover the current relay process. Run this example in `link/grpc`.

```sh
grpcurl -cacert /etc/rutis/grpc/ca.pem -cert /etc/rutis/grpc/operator.pem \
  -key /etc/rutis/grpc/operator.key -authority directory.test \
  -import-path . -proto wire/cloud.proto -d '{}' 127.0.0.1:7443 rutis.link.v1.Cloud/Inspect
```

## Failover and deployment boundaries

When the preferred region has fewer than two slots available, the directory tries other permitted regions. If a relay reports capacity exhaustion while redeeming the first ticket, the directory invalidates both outstanding tickets. If every online permitted region is full, it returns `ResourceExhausted / STREAM_QUOTA`. A pair with an admitted member keeps its region for its reciprocal member or a replacement. Regional agents renew every 4 s. After 12 s without renewal the relay ends waiting and admitted streams even if both data sockets remain reachable. The directory can place the next generation in another permitted region. It returns `Unavailable` when all permitted regions are offline. A client-specific path failure retries the assigned region while that region continues to renew its lease.

A fresh stream repeats the rutis handshake. References, callbacks and plugin instances owned by the old session are released. An interrupted RPC can already have had side effects. The caller receives an error and the link never replays the call. Applications that retry writes can deduplicate by request ID. Services mounted separately on a host retain their own lifecycle.

Run one active directory with a persistent `stateFile` for monotonically increasing generations. A region admits one leased relay instance. On Kubernetes, route each advertised address to that instance and give the directory an exclusive persistent volume. The trusted relay terminates both TLS hops and can read payloads. The directory handles registration, routing and counters. The residency list constrains the forwarding region. Select an appropriate location for the directory's own metadata separately.

Both hops require native HTTP/2 bidirectional streaming, trailers and room for a 16 MiB payload plus protobuf overhead. The relay processes one forwarding operation per direction. A blocked write propagates backpressure. The client probes idle connections with an active stream after 5 min, with a 20 s timeout. Configure proxy stream lifetime, buffering, idle timeouts and connection draining for long-lived streams. This transport targets always-on hosts and service meshes. Browser clients can use WebRTC. [gRPC flow control](https://grpc.io/docs/guides/flow-control/) · [gRPC keepalive](https://grpc.io/docs/guides/keepalive/)
