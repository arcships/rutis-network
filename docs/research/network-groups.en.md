# Discovery, network formation and the boundary between network and application services

[中文](network-groups.md)

A device may be the only party able to dial, yet the cloud can call it back over the established connection. Other networks have a genuinely one-way channel, or endpoints that will never be online together. Three questions help distinguish these cases. Who can discover whom? What does a message address? Under what conditions can the parties complete an exchange?

This research uses rutis 0.23.0 as its baseline. Here the network layer includes the discovery, reachability and transport plugins in `rutis-network`, a broader scope than IP. Existing implementations illustrate the boundary. Optional services and proposed experiments describe the remaining design space.

The [network stack survey](network-stacks.en.md) applies these boundaries to IoT, industrial and specialized networks by inspecting the IP interface, duplex stream, attributes, short messages or one-way observations a device exposes.

For cross-domain trust, authorization during a partition and path recovery, see [security, reliability and trustworthiness](network-assurance.en.md).

## 1. What rutis already provides

- A link can hand an admitted, reliable, ordered, bidirectional message channel to `rutis/sessions.open(far)`. The link row owns admission, framing and the connection lifecycle. Rutis learns the peer's self-declared ID from the session Hello and manages remote plugins, service scope and online events. A service can already reach a third node through an intermediate host. Providers with the same service name in one scope can conflict at registration. [Link plugin contract](../design/01-link-plugin-contract.en.md) · [Connecting rutis nodes](https://github.com/arcships/rutis/blob/v0.23.0/docs/guide/nodes.en.md)
- The Go Tailscale plugin watches the network map visible to its own node, selects peers by tag and maintains connections by stable Tailscale node ID. A rename can preserve a connection. Removing the tag closes it. This observation has a specific source and scope. Applications define tenant, workshop and job-queue membership. [Tailscale implementation and configuration](../design/04-link-tailscale.en.md)
- The Go libp2p plugin supports direct and relayed connections to a specified peer. The Go gRPC plugin connects a device to a plugin embedded in a cloud service. In the gRPC example the transport carries the request, and the cloud application's scheduler handles the job. Changing the path to the cloud leaves the scheduler in charge of that decision. [libp2p plugin](../design/09-p2p-plugins.en.md) · [gRPC implementation and design scope](../design/07-link-grpc.en.md)

## 2. Deriving capabilities from network types

Topology, administrative boundaries and availability over time are independent dimensions. A factory can use a mesh inside a workshop, connect to other factories through a gateway and upload data whenever the cloud uplink is available.

| Network type | What a plugin can observe from its location | Capability implied |
| --- | --- | --- |
| LAN or proximity network | Service instances appear and disappear on the local link. Addresses can change. | Browse by type, resolve addresses and expire records. Check admission when connecting. [DNS-SD](https://datatracker.ietf.org/doc/html/rfc6763) |
| Internet or cellular star | Devices can dial out and the hub can call back. A path between devices needs its own check. | A known ingress address, calls over an established connection, and introductions or relaying when required. [gRPC example](../guide/link-plugins.en.md) |
| Tree or hierarchy | An upstream node may know only the factory entrance. A downstream node knows its workshops and devices. | Address a domain or prefix, select a next hop or delegate to a downstream gateway. |
| Enterprise VPN or overlay | A node can participate in several networks, each with its own names, tags and visibility. | Identify the network used for observation and dialing. Reuse its directory and admission policy. [Tailscale plugin](../design/04-link-tailscale.en.md) |
| Cluster service network | A stable service name can represent several interchangeable backends. | Select the service endpoint or enumerate instances. Network reachability and application job requirements offer different selection criteria. [Kubernetes Service](https://kubernetes.io/docs/concepts/services-networking/service/) |
| Peer-to-peer or self-organizing mesh | Nodes know a subset of their neighbors. Several paths may lead to a destination. | Bootstrap, rendezvous, peer discovery and path selection. Hop-by-hop dissemination also needs bounds on scope and duplicate forwarding. [libp2p DHT](https://libp2p.io/docs/kademlia-dht/) |
| Topic network | The publisher knows a topic. The subscribers change over time. | Distribute by subscription and specify scope and delivery guarantees. A plugin can bridge rutis online events to an external topic network. [libp2p Pub/Sub](https://libp2p.io/docs/pubsub/) |
| Independently administered networks or federation | Each domain controls names and admission. An outsider may see only one ingress. | Qualify names by origin, selectively publish and withdraw records, and enforce admission at the boundary. Discovering a record and reaching the service are separate operations. [Cross-link discovery proxy](https://datatracker.ietf.org/doc/html/rfc8766) |
| Intermittent or opportunistic network | A meets B and B later meets C. There may be no instant when all three are online. | Store and forward, apply expiry and record receipt at each hop. The application separately confirms processing. [Bundle Protocol](https://datatracker.ietf.org/doc/html/rfc9171) |
| One-way channel | A receiver hears announcements or data. The channel has no return path. | Expose subscription or reception. Receipts require an independently agreed return path, and live bidirectional RPC requires another channel. |

**The destination shapes the interface.** Unicast addresses one node. Choosing any reachable instance addresses a pool. Broadcast addresses receivers on a link. Multicast and topic delivery address a group or subscription. Collecting replies from the group adds a membership snapshot, a deadline and partial results. Addressing a domain delegates the choice of recipient. Requesting an object by hash allows a verified replica to respond. Addressing an offline recipient separates its identity from delivery time. A membership group, subscriber group, service pool and administrative domain each has its own rules for joining and leaving. [IP multicast](https://datatracker.ietf.org/doc/html/rfc1112) · [Content naming](https://named-data.net/project/archoverview/)

## 3. What an intermediate node does

Consider B on the path `A → B → C`. One machine can perform several roles.

| B's role | B's decision | Consequence for the endpoints |
| --- | --- | --- |
| Bootstrap, rendezvous or directory | Supply initial neighbors, introduce peers or resolve a name to candidate addresses. | A and C may connect directly after the introduction. Candidates still need a connectivity check. [ICE connectivity checks](https://datatracker.ietf.org/doc/html/rfc8445) |
| Bridge, router or relay | A bridge forwards frames between link segments. A router chooses a next hop for a destination. A transparent relay forwards data between two available connections. | When B only forwards, A and C remain the rutis session endpoints. Existing infrastructure can provide IP routing. [libp2p circuit relay](https://libp2p.io/docs/circuit-relay/) |
| Gateway or protocol translator | A gateway is an entrance to or exit from a domain. A router can fill this role. An application gateway can also terminate an external request and make a separate internal call. | Such an application gateway is A's peer. It can expose one interface while keeping C's address and protocol internal. |
| Broker, multicast forwarder or gossip peer | Replicate by subscription, distribution tree or neighbor relationships, respectively. | A sender can address a group or topic. Consumers need to know the scope, duplicate behavior and expiry rules. |
| Mailbox, cache or replica | Hold data for an offline recipient, return a stored copy by content or serve reads while the origin is offline. | Receipt at the intermediary, delivery to the destination and successful application processing each require distinct evidence. |
| Path probe, selector or observer | Test a path from a specific location, compare direct and relayed paths, or switch egress according to configuration. | A failed probe describes that observer's path. Switching paths can require a new session. |

## 4. Network infrastructure and application services

Ask **who can verify the promised outcome**. A link row can report that it admitted a connection to an address at a given time. A durable mailbox can confirm that it stored a message. The order application can confirm that it posted an order. Each receipt has a distinct meaning even when all three components run in one Go process.

**Network policy has an owner of its own.** For example, a link plugin can maintain a channel using an existing Cloudflare Tunnel and its credentials. The operator configures which site uses the tunnel and who manages the Access policy. The order service checks whether the caller may approve an order. A single request can pass through all three decisions. [Cloudflare plugin management boundary](../design/05-cloudflare-plugin-control.en.md)

| Capability | Network infrastructure owns | Application plugin or operator decides |
| --- | --- | --- |
| Discovery and membership | Publish, resolve, observe and withdraw candidate endpoints in a named network. Record source, validity and observation point. | Which devices belong to a site, tenant or fleet, who may assert membership, and whether it survives loss of connectivity. |
| Connecting and forming networks | Connect according to declared peers, tags or topologies. Enforce link admission and handle heartbeat, backoff, release and path state. | Which sites should connect, permitted egresses, cost and resource budgets, and authorization for application methods. |
| Multiple providers | Return matching candidates, optionally select a reachable ingress and report refusals or timeouts. | Select an OCR worker by model version, customer requirements or load. Own queuing, job retries and idempotency. Reconnecting cannot establish whether the previous job ran. |
| Cross-domain gateway | Qualify the source domain, publish or withdraw configured records, enforce boundary admission or forward transparently. | Choose which capabilities to share with each partner. An application gateway enforces method permissions, converts units or filters fields when it terminates a request. |
| Group messaging and fan-out | Optional plugins track subscriptions, bound dissemination and report the delivery status they can establish per destination. | Define alert topics and audiences, fix the membership snapshot, choose all replies or a quorum, handle partial results, and decide whether a duplicate alert takes effect again. |
| Deferred delivery and content distribution | Optional plugins accept, retain or expire data under their stated persistence guarantees, or return verifiable copies. | Set order validity, choose acceptable data versions, confirm consumption and handle duplicate or expired operations. |
| Observability | Identify the observer, time, state and failure cause for a path or queue. | Decide whether a device has stopped production, a job has missed its deadline, or an operator should intervene. |

**Placement follows the guarantee.** An adapter owns admission, I/O, shutdown and status for its link. A directory, rendezvous service, relay, discovery proxy, topic bridge or mailbox is **optional infrastructure for a particular network**. Each can run independently or be injected as an ordinary plugin service. Schedulers, inventory queries, tenant authorization, alerts and business receipts live in application plugins. Rutis already owns service registration, dependency lifecycle and online events once a session opens. [Plugin composition contract D11](../design/01-link-plugin-contract.en.md) · [Connecting rutis nodes](https://github.com/arcships/rutis/blob/v0.23.0/docs/guide/nodes.en.md)

A Go discovery plugin could use its configuration row to choose the network to observe and the local service name to provide. Consumers would declare that service in `Inject`. An authorized management plugin can use `rutis/loader` to add or remove connection rows. Each link row retains ownership of its certificates, tokens and retries. To share a narrow cross-domain interface, a boundary plugin can terminate requests and explicitly export operations. A direct rutis node session trusts the admitted peer. Filtering discovery results alone cannot limit an established session. [Existing management pattern](../design/04-link-tailscale.en.md) · [Host trust boundary](https://github.com/arcships/rutis/blob/v0.23.0/docs/guide/nodes.en.md#security)

## 5. Research questions with observable outcomes

These proposed experiments can use independent Go plugins alongside the existing links.

1. **Proximity discovery.** A discovers B's advertised collection service, but B currently refuses admission. A should distinguish a valid record from the refused connection attempt. When the record expires, the discovery view removes it. The application still owns its device roster.
2. **Two administrative domains.** Two devices in domain A provide the same collection service name in their own scopes. A announces a single ingress to domain B. Disconnect the inter-domain path. B can report the failed path and retain its last observation with an expiry. Restore the path, then explicitly withdraw the ingress. B should observe withdrawal while the second service remains private. Use the duplicate service names to test domain scope and verify that external callers can invoke only the methods explicitly exported at the ingress.
3. **Two paths and an in-flight call.** A and C have direct and relayed paths. Cut the direct path after job submission but before the application receipt. The network can report failure and establish another session. The application can look up the job by ID or retry safely. Check path recovery and the actual number of job executions separately.
4. **Asynchronous delivery.** A leaves a message with B and goes offline. C appears the next day. Deliver one valid message and let a second expire. Observe B's durable receipt, delivery to C and the outcome of application processing separately. Check that duplicate delivery does not execute the business operation twice.

## 6. A plugin design for discovery and network formation

Consider one controller and ten collectors in a workshop. Each collector needs one connection to the controller. The controller can call back over the established session. All eleven devices can be visible on the LAN, but there is no reason to maintain the 55 connections of a full mesh. **Discovery reports candidate endpoints. Group policy decides which connections this host should maintain. A link implements each selected connection.** Each host only needs its part of the topology.

Introduce two independently deployable Go plugins. Their names, discovery interface and policy configuration below are proposals. The existing TCP control interface already has the necessary operations. [TCP controls](../design/06-link-tcp-integration.en.md) · [Plugin composition contract D11](../design/01-link-plugin-contract.en.md)

This example follows one collector connecting to the workshop controller. Dotted arrows carry advertisements, policy decisions and link status. Solid arrows trace application requests and replies. Once mTLS admission succeeds, the link passes the channel to `rutis/sessions.open(far)`.

```mermaid
flowchart TB
    advert["Controller advertises its LAN ingress"] -. DNS-SD / mDNS .-> discovery["Discovery plugin · go:discover-mdns"]
    discovery -. Candidate addresses, interface and expiry .-> group["Group policy plugin · go:peer-group"]
    policy["Collector's local policy"] -. Target, allowed subnet and expected identity .-> group
    group -. Configure target, start or stop .-> link["Link plugin · go:link-tcp-dial"]
    link -. Link state and latest error .-> group
    app["Collector application plugin"] <-->|Call by service name| session["Local rutis session"]
    session <-->|Bidirectional messages| link
    link <-->|mTLS connection| peer["Controller link and rutis session"]
```

| Component | Responsibility of one configured row | Consumer |
| --- | --- | --- |
| `go:discover-mdns` | Browse DNS-SD services on the specified interface and DNS domain, or advertise a locally configured ingress. Provide a named read-only service such as `shop-discovery`. | A UI can display candidates independently. A group policy can subscribe to the same observations. |
| `go:peer-group` | Declare the intended peers, allowed discovery source and dedicated dialing service. Continuously compare the intent with observed link state and initiate or stop connections as needed. | A management UI can inspect the selection reason, transport state and latest error for each target. Application plugins still use rutis service names. |
| Existing link row | Dial the address, authenticate the expected peer, handle retries and own the session lifecycle. The deployment supplies credentials to this row. | A group row operates only the explicitly injected local control services it exclusively owns. |

### Candidates, targets and link state

A **candidate endpoint** answers “where did I observe this ingress?” The discovery service exposes `snapshot()` and `waitForChange(cursor)`. Both return the latest full snapshot in the same format. Each record includes its source row, interface, DNS domain, service type, instance, transport, resolved addresses and port, and expiry. Key a record by source, interface, domain, service type and instance. A record can have several addresses. Keep addresses scoped to their interface, including the zone of a link-local IPv6 address. Expire individual addresses or the whole entry when the underlying records expire. Full snapshots let slow readers catch up. A restart creates a new cursor. The publishing row advertises its configured ingress and withdraws it on unload. TTL covers abrupt power loss. [DNS-SD](https://www.rfc-editor.org/rfc/rfc6763.html) · [mDNS](https://www.rfc-editor.org/rfc/rfc6762.html)

A **connection target** answers “who is this host allowed to connect to, and which side dials?” Give it a stable local name, candidate matching rules, permitted address ranges, an expected transport identity and a dedicated link control service. Discovery instance names and TXT labels are selection hints. Obtain the expected TLS name from local configuration. Keep the CA and private key in the TCP row. Matching names, addresses or labels are insufficient to merge endpoints across networks into one member. An application that requires authoritative membership can supply its roster as a policy input. A LAN advertisement cannot grant membership.

**Connection state** has three independently observable parts per target. The discovery record can be current, the link can pass local admission and attach, and an application service can become callable. The group row can expose the first two, the chosen address and the last error via `status()` and `waitForChange(cursor)`. Service registration or a successful call supplies evidence for the third. Go's `sessions.open` does not return the rutis ID from the handshake. Advertisements and certificates cannot supply that ID. [TCP status fields](../design/06-link-tcp-integration.en.md)

The model can express three common topologies. They differ in the targets each host selects and the edges it keeps.

| Topology | Connections this host should maintain |
| --- | --- |
| Star | Each leaf keeps one edge to its designated hub. The hub accepts multiple inbound connections. |
| Bounded mesh | Each node connects to a subset of its permitted neighbors. Policy limits the count and designates one dial owner per edge. Local neighbor visibility alone cannot guarantee a connected graph. |
| Network-to-network | Domains A and B each designate a boundary ingress and maintain an edge between them. Deployment policy defines which prefixes or services may be advertised. For limited sharing, terminate calls at a gateway, explicitly export allowed interfaces and isolate internal services. |

### First configuration and lifecycle

Start with each leaf dialing a hub whose identity is known locally. This is a **proposed leaf configuration**. Discovery and dialing services are isolated in the local management scope. The deployment supplies the TCP row's credentials. The policy selects an address and port when the endpoint changes.

```json
{
  "rows": [
    {
      "id": "browse-shop",
      "name": "go:discover-mdns",
      "isolate": {"shop-discovery": "management"},
      "config": {"service": "shop-discovery", "interface": "eth0", "domain": "local.", "browse": "_rutis._tcp"}
    },
    {
      "id": "to-hub",
      "name": "go:link-tcp-dial",
      "isolate": {"hub-link": "management"},
      "config": {"controlService": "hub-link", "autoStart": false, "ca": "/etc/rutis/ca.pem", "cert": "/etc/rutis/leaf.pem", "key": "/etc/rutis/leaf.key"}
    },
    {
      "id": "shop",
      "name": "go:peer-group",
      "inject": ["shop-discovery", "hub-link"],
      "isolate": {"shop-discovery": "management", "hub-link": "management", "shop-status": "management"},
      "config": {"source": "shop-discovery", "service": "shop-status", "targets": [{"id": "controller", "instance": "shop-hub", "allowedCIDRs": ["192.168.20.0/24"], "servername": "hub.shop.example", "link": "hub-link"}]}
    }
  ]
}
```

The controller runs a publishing row and announces instance `shop-hub` with an actual LAN ingress, for example `192.168.20.8:7443`. The ingress must carry the mTLS byte stream through to the controller, where leaf certificates are checked. The current Go TCP listener binds only to IPv4 loopback. For a two-host trial, provide a configured LAN TCP forwarder to that loopback port. Advertise when the ingress is ready and withdraw when it stops. The controller listens and each leaf dials, giving the controller ten bidirectional connections. [Existing listener and forwarding options](../guide/link-plugins.en.md) · [TCP listener restriction](../design/06-link-tcp.en.md)

A leaf validates the source and the resolved IP against its permitted range, then calls `configure({host, port, servername})` and `start()` on `hub-link`. Pass the checked IP as `host` and the locally configured name as `servername`. Serialize decisions per target using the latest discovery and link snapshots. Repeated announcements only refresh the expiry. With multiple addresses, keep a healthy path and select a still-valid address after disconnect. The TCP row backs off on transient failures. The group row avoids parallel attempts for the same target. After a service reload, read both snapshots before taking action. Start with one preconfigured dial row per target and validate ordinary cross-host service calls. [Existing TCP configuration and retry behavior](../design/06-link-tcp-integration.en.md)

| Event | Default behavior and acceptance criterion |
| --- | --- |
| Another interface announces the same name, or the resolved IP is outside the permitted range | Dial only candidates on the configured interface and in the permitted range. A valid address with the wrong certificate still fails TCP admission. |
| The advertisement disappears while an established link is healthy | Remove the record and disable automatic retries for the target. The established connection can continue. Stop a pending dial or backoff. After a later disconnect, wait for a fresh record. Verify expiry and connectivity independently. [TCP retry control](../design/06-link-tcp-integration.en.md) |
| The address changes, the interface briefly goes down or the publisher restarts | Keep a healthy connection and update candidates for the next dial. Stop attempts on the old address before selecting another after disconnect. Resolve duplicate or out-of-order notifications against the latest snapshot. |
| Local policy revokes the target or the group row unloads | Immediately stop owned dials and retries, close active links and cancel observations. Other groups and manually configured links continue operating. |

A host can participate in several groups, each with explicit sources, targets and control services. The same discovery interface could later read a static directory. The target model can describe a set of configured neighbors. Define one dial owner per edge before introducing a bounded mesh. Existing Tailscale tag discovery retains its authoritative network map and revocation rules. Across domains, a directory introduces endpoints, a relay forwards along a selected path, a router selects a next hop and a protocol gateway terminates the native protocol and exports a service. Deploy those roles as needed. The LAN group plugin only has to maintain this host's selected edges. [Tailscale tag discovery](../design/04-link-tailscale.en.md) · [libp2p direct and relay paths](../design/09-p2p-plugins.en.md)
