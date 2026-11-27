# Discovery, network formation and the boundary between network and application services

[中文](network-groups.md)

A device may be the only party able to dial, yet the cloud can call it back over the established connection. Other networks have a genuinely one-way channel, or endpoints that will never be online together. Three questions help distinguish these cases. Who can discover whom? What does a message address? Under what conditions can the parties complete an exchange?

This research uses rutis 0.23.0 as its baseline. Here the network layer includes the discovery, reachability and transport plugins in `rutis-network`, a broader scope than IP. Existing implementations illustrate the boundary. Optional services and proposed experiments describe the remaining design space.

The [network stack survey](network-stacks.en.md) applies these boundaries to IoT, industrial and specialized networks by inspecting the IP interface, duplex stream, attributes, short messages or one-way observations a device exposes.

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
