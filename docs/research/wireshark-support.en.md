# Which Wireshark protocol families should rutis-network support?

[中文](wireshark-support.md) · [Per-filter inventory (CSV)](wireshark-protocols.csv) · [Network stack survey](network-stacks.en.md)

**Start with local service discovery.** Two plugins can already establish sessions over TCP, SSH or gRPC. The libp2p plugin already supports direct QUIC and relays. Tailscale also discovers tagged nodes within a tailnet. An ordinary LAN still needs a standalone way to find a dialable plugin and tell when its record has expired. Devices and brokers present another gap. A UDP instrument delivers datagrams and an MQTT broker delivers topic messages. The device itself can run its native protocol throughout. [Tailscale tag discovery](../design/04-link-tailscale.en.md) · [Network groups and service boundaries](network-groups.en.md) · [Existing libp2p paths](../design/09-p2p-plugins.en.md)

## Five capabilities to validate

The ordering below is a research recommendation. Each row describes a small, independently testable Go plugin capability. The filter key identifies an actual index entry, while a capability may use several protocols.

| Priority and filter key | Plugin interface | Acceptance experiment |
| --- | --- | --- |
| **First · `dns`** | Advertise, browse and withdraw candidate endpoints with DNS-SD. Use mDNS on the LAN. Preserve interface, instance, address, port and expiry. The caller selects a candidate and the existing link handles admission when dialing. | Two hosts can discover and dial each other. Distinguish identical names on different interfaces and remove records on withdrawal or TTL expiry. A peer that advertises a record still has to pass admission. [DNS-SD](https://www.rfc-editor.org/rfc/rfc6763.html) · [mDNS](https://www.rfc-editor.org/rfc/rfc6762.html) |
| **First · `udp`** | Expose a datagram endpoint for UDP or multicast equipment. Bind a configured address and interface. Deliver one complete datagram with its source per receive, with limits on size, send rate and receive queue. | Distinguish two senders. Use numbered test payloads to check loss, duplication and reordering. Reject truncated messages and count local queue drops. Leave device-defined sequence numbers to the device adapter. [UDP usage guidelines](https://www.rfc-editor.org/rfc/rfc8085.html) |
| **First · `mqtt`** | Publish and subscribe on an existing broker. The protocol plugin owns connection, subscription recovery, QoS and retained flags. The application owns topic conventions, payloads, device identity and command receipts. | Fan out to two ordinary subscribers. On resubscription, preserve the broker's retained flag and judge freshness from device timestamps. Exercise QoS 1 redelivery and observe the device's separate command receipt. [MQTT 5](https://docs.oasis-open.org/mqtt/mqtt/v5.0/os/mqtt-v5.0-os.html) · [Worked device example](communication-protocols.en.md) |
| **With a constrained device · `coap`** | Send requests to a resource. Subscribe when the peer supports Observe. Preserve the response code, resource identity and freshness information. | Exercise duplicate requests, timeout, observation cancellation and an offline device. An empty ACK cannot complete an operation that needs the resource result. [CoAP](https://www.rfc-editor.org/rfc/rfc7252.html) · [Observe](https://www.rfc-editor.org/rfc/rfc7641.html) |
| **With an Android peer · `adb`** | Follow the mobile design and use authorized ADB port forwarding with the existing TCP bridge. The device must be running a peer plugin. | Complete a remote call across two rutis hosts. Reject an unauthorized device, close the session on unplug and release the forwarding rule when its configuration stops. [Mobile design PR #18](https://github.com/arcships/rutis-network/pull/18) · [ADB](https://developer.android.com/tools/adb) |

**WebRTC is implemented.** Browser and native Go peers can establish a rutis session over a reliable, ordered DataChannel. The deployment authorizes pairing and provides signaling. ICE can select a direct path or a TURN relay. Verify calls in both directions on each path during integration. [WebRTC plugin and acceptance checks](../design/08-link-webrtc.en.md)

**Each network role makes a distinct promise.** DNS-SD yields a candidate address with an expiry. A router chooses the IP next hop. A transparent relay forwards traffic while admission and endpoint identity are verified at their respective layers. An MQTT or Zigbee gateway can terminate the native protocol and deliver a topic message or device event to rutis. Group membership, tenant assignment, temperature units and command completion belong to the application. A cloud service can embed one of the two peer plugins in a gRPC session. [Cloud-service gRPC plugin](../design/07-link-grpc.en.md) · [Security, reliability and trustworthiness](network-assurance.en.md)

## Coverage and decision rules

The input is the alphabetical Wireshark Display Filter Reference retrieved on 2026-11-27, labeled **4.6.10**. Its introduction describes decoder coverage as approximately 3,000 protocols. The index itself enumerates **2,166 distinct filter keys**. The listed version ranges include 4.6.10 for **2,056** keys and end at an earlier release for **110**. These entries include subprotocols, device clusters, capture formats and analyzer pseudo-protocols. For example, individual Zigbee clusters have filters and `_ws.expert` exposes analyzer information. The count covers the published index only. Protocols absent from it, other releases and third-party plugins are outside this snapshot. [Official index](https://www.wireshark.org/docs/dfref/) · [4.6.10 dissector sources](https://github.com/wireshark/wireshark/tree/v4.6.10/epan/dissectors)

The CSV assigns each key one primary family and one decision, preserving its upstream name, full version ranges and original relative detail-page path. The **50 protocol families and related functional groups** in `family` are this survey's taxonomy. Cross-layer entries follow the main handoff. For example, ADB is with USB and device channels, and the Bluetooth commissioning filters for Matter are with Matter. Older entries retain their family and receive the `historical` decision. The family matrix explains the default approach and the CSV records individual exceptions. Two easily confused keys are `pcp` for performance monitoring and `portcontrol` for NAT port control. [Per-filter inventory](wireshark-protocols.csv) · [Performance Co-Pilot](https://www.wireshark.org/docs/dfref/p/pcp.html) · [Port Control Protocol](https://www.wireshark.org/docs/dfref/p/portcontrol.html)

| CSV decision | Entries | Meaning for rutis-network |
| --- | ---: | --- |
| `existing-link` | 6 | This repository implements TCP, SSH, gRPC and WebRTC. QUIC is available through libp2p. The rutis host provides WebSocket. Products and filters are counted separately. [Link contract](../design/01-link-plugin-contract.en.md) |
| `trial-link` | 1 | Test the session contract over authorized ADB forwarding to a peer plugin on the device. |
| `trial-service` | 4 | DNS service discovery, UDP datagrams, MQTT topics and CoAP resources each justify a native service interface. |
| `reuse` | 779 | Consume the host, library, daemon or network service, for example TLS, BGP or STUN. |
| `gateway` | 406 | Integrate on demand for a named device, controller or broker. One gateway often covers many filters. |
| `application` | 607 | An application or industry plugin owns the promise, such as a file write, database transaction or call state. |
| `reference` | 238 | Useful for diagnostics, decoding or legacy interoperability. No dedicated project proposed. |
| `defer` | 15 | Eleven delay-tolerant or space transport entries need a disconnected-path use case. Four current keys lack an established plugin handoff. |
| `historical` | 110 | The upstream version range ends before 4.6.10. Filter history alone cannot establish whether the underlying protocol remains deployed. |

## Protocol families

“Current / all” counts keys whose ranges include 4.6.10 and all keys in the group, respectively. The example keys can be looked up directly in the CSV.

### Connectivity, discovery and forwarding

| Family (CSV key) | Current / all | Example keys | Handoff to rutis |
| --- | ---: | --- | --- |
| Transport and reliable multicast · `transport` | 28 / 28 | `tcp`, `quic`, `udp`, `sctp`, `pgm` | TCP and QUIC already carry sessions. UDP preserves datagram boundaries. SCTP and reliable multicast need a specific peer and delivery requirement. |
| Web, proxies and streaming RPC · `web` | 17 / 17 | `http2`, `grpc`, `websocket`, `http3` | Use the existing gRPC bidirectional stream or host WebSocket. Expose unary HTTP calls as application methods. |
| WebRTC data channels · `webrtc` | 1 / 1 | `rtcdc` | Use the existing WebRTC plugin for reliable, ordered session messages over a direct or TURN-relayed path. [Plugin guide](../design/08-link-webrtc.en.md) |
| Remote terminals, desktops and proxies · `remote` | 26 / 26 | `ssh`, `rdp`, `vnc`, `socks` | SSH already carries sessions. Screen updates, clipboard and input belong to a remote-control service. |
| Naming, address configuration and discovery · `discovery` | 24 / 25 | `dns`, `dhcp`, `srvloc` | Start with DNS-SD. Leave DHCP to the host and scope vendor discovery records to the network that produced them. |
| NAT traversal and relaying · `traversal` | 5 / 6 | `stun`, `turnchannel`, `portcontrol` | Use a traversal library or TURN service within the link. Report whether the selected path is direct or relayed. |
| Tunnels, VPNs and overlays · `overlay` | 34 / 34 | `wg`, `esp`, `vxlan`, `geneve` | Consume reachability through an interface, daemon or SDK. Deployment owners control routes and credentials. |
| IP, routing, multicast and path liveness · `routing` | 86 / 90 | `bgp`, `ospf`, `pim`, `igmp`, `bfd` | Let routers choose next hops. Plugins may observe paths or join multicast groups, while membership authorization remains separate. |
| Authentication, keys and transport protection · `auth` | 60 / 62 | `tls`, `dtls`, `radius`, `oscore` | Use established libraries at admission. Validate the certificate, network account and rutis Hello identity at their respective boundaries. |

### Network access and device interfaces

| Family (CSV key) | Current / all | Example keys | Handoff to rutis |
| --- | ---: | --- | --- |
| Ethernet, switching and VLANs · `lan` | 74 / 75 | `eth`, `vlan`, `lldp`, `stp` | Use the NIC and switch. LLDP can supply adjacency hints. |
| Wireless LANs and mesh · `wifi` | 50 / 53 | `wlan`, `wifi_p2p`, `batadv` | Use the OS to join the network, then dial an address. The wireless mesh owns its neighbor table. |
| Broadband and carrier access · `access` | 123 / 142 | `docsis`, `pppoe`, `atm`, `homeplug` | A modem or access device provides the interface. Plugins usually receive IP connectivity or a device API. |
| Cellular, mobile core and telecom signaling · `cellular` | 186 / 205 | `nas-5gs`, `ngap`, `gtp`, `gsm_map` | Use the terminal data connection. Obtain SMS or network events through the operator interface with its delivery scope. |
| InfiniBand, RDMA and compute fabrics · `fabric` | 14 / 16 | `infiniband`, `irdma`, `ipoib` | Reuse an existing link over IP. Native queue pairs need a measured benefit and a resource-release model. |
| Low-rate wireless and IPv6 adaptation · `pan` | 3 / 3 | `wpan`, `6lowpan`, `wisun` | A border router can expose IPv6. Test low-power and sleep constraints with the chosen transport. |
| Thread mesh networking · `thread` | 9 / 9 | `mle`, `thread_meshcop`, `trel` | Use the existing Thread network and border router, then connect to the port the device actually exposes. |
| Zigbee, RF4CE and device clusters · `zigbee` | 83 / 88 | `zbee_nwk`, `zbee_aps`, `zbee_zcl` | Read endpoints, attributes and events through a coordinator. Many clusters can share one gateway. |
| Matter fabrics and commissioning · `matter` | 3 / 3 | `matter`, `btp-matter` | Use an authorized controller for attributes, commands and subscriptions within its fabric. |
| LoRaWAN low-power wide-area networking · `lpwan` | 1 / 1 | `lorawan` | Use the network server for uplinks and queued downlinks. Preserve receive time, expiry and downlink outcome. |
| Bluetooth and BLE profiles · `bluetooth` | 54 / 56 | `bluetooth`, `btatt`, `btgatt`, `btrfcomm` | Use the platform Bluetooth stack. Expose characteristics, notifications or a serial endpoint according to the profile. |
| Contactless cards, readers and RFID · `nfc` | 8 / 8 | `iso14443`, `felica`, `llrp` | A reader plugin exposes a transaction or tag event and preserves separate reader and card identities. |
| USB, device buses and debugging channels · `usb` | 23 / 24 | `usb`, `adb`, `adb_cs` | Let the OS drive USB. Authorized ADB forwarding can reach a peer plugin running on the device. |
| Printing, scanning and peripherals · `peripheral` | 16 / 17 | `ipp`, `sane`, `i2c` | A gateway exposes print jobs, scan results or bus operations with device-defined acknowledgments. |
| Device objects, location and vendor IoT · `iot` | 27 / 27 | `dof`, `aj`, `rtls` | Consume the controller's native objects and events. Define object mapping per deployment. |

### Messaging, control and delayed delivery

| Family (CSV key) | Current / all | Example keys | Handoff to rutis |
| --- | ---: | --- | --- |
| Pub/sub, queues and data distribution · `messaging` | 28 / 29 | `mqtt`, `mqttsn`, `amqp`, `kafka`, `rtps` | Prioritize the MQTT boundary. Integrate AMQP, Kafka or DDS/RTPS for a specific deployment and preserve settlement, offsets or QoS. |
| Constrained device resources · `resource` | 2 / 2 | `coap`, `lwm2mtlv` | CoAP can expose a generic resource interface. The caller or gateway owns models, units and actions. |
| Industrial control, fieldbus and robotics · `industrial` | 82 / 85 | `modbus`, `opcua`, `cip`, `pn_io`, `tcpros` | Read registers, nodes or events through the controller or endpoint. Keep control loops and command interlocks on site. |
| Building automation · `building` | 10 / 11 | `bacnet`, `kip`, `mstp` | Use BACnet objects or KNX group addresses. The project configuration defines what each point means. |
| Power, metering and telecontrol · `utilities` | 19 / 21 | `dnp3`, `goose`, `cosem`, `synphasor` | Map measurements, timestamps and quality at the gateway. The field system owns protection actions and real-time multicast. |
| Instrumentation and semiconductor equipment · `instrument` | 7 / 10 | `hislip`, `hsms`, `vxi11_core` | Use the instrument controller and preserve its command-completion and measurement semantics. |
| Vehicle, rail and road communication · `vehicle` | 44 / 44 | `can`, `j1939`, `doip`, `someip` | Use the vehicle gateway for signals or diagnostics and retain the source bus and message identity. |
| Delay-tolerant networking and space data transfer · `delay` | 11 / 11 | `bpv7`, `tcpcl`, `ltp`, `cfdp` | Wait for a disconnected-path requirement, then expose a bundle queue with expiry, forwarding and delivery state. |

### Application and domain protocols

| Family (CSV key) | Current / all | Example keys | Handoff to rutis |
| --- | ---: | --- | --- |
| Network operations, measurement and time · `ops` | 79 / 84 | `snmp`, `ntp`, `ptp`, `syslog`, `pcp` | Consume existing clocks and monitoring systems. Use a management endpoint for device metrics or operations. |
| Enterprise RPC, directories and clusters · `enterprise` | 162 / 164 | `dcerpc`, `ldap`, `giop`, `thrift` | Wrap directory queries or RPC methods in the application plugin with the original access controls. |
| Databases, caches and data services · `database` | 16 / 16 | `pgsql`, `mysql`, `cql`, `memcache` | The application driver owns queries, transactions and retry behavior. |
| Filesystems, block storage and storage networking · `storage` | 73 / 75 | `nfs`, `smb2`, `iscsi`, `nvme` | The storage system or its client owns mounts, locks, write acknowledgment and recovery. |
| File exchange and peer distribution · `file` | 30 / 30 | `sftp`, `tftp`, `bittorrent`, `bt-dht` | Expose a file or content ID in the application. A protocol's DHT serves its own namespace. |
| Mail, chat and presence · `collab` | 51 / 55 | `smtp`, `imap`, `xmpp`, `msrp` | Use account, room or mailbox APIs. Presence and read receipts retain their application meaning. |
| Telephony signaling and real-time sessions · `voice` | 73 / 78 | `sip`, `sdp`, `rtp`, `rtcp` | Expose call state or media quality. RTP permits loss and cannot directly carry a reliable rutis session. |
| Audiovisual, broadcast and show control · `media` | 114 / 122 | `rtsp`, `srt`, `h264`, `artnet` | Players and consoles own codecs, timing and lighting channels. |
| Trading, payments and digital assets · `finance` | 17 / 17 | `fix`, `nasdaq_itch`, `iso8583`, `bitcoin` | The financial application interprets sequence numbers, market-data distribution and settlement. |
| Clinical data and notifications · `medical` | 3 / 4 | `dicom`, `hl7` | Integrate imaging, messages or alerts. The application owns record identifiers and authorization. |
| Aviation, navigation and aerospace telemetry · `aviation` | 18 / 18 | `asterix`, `atn-cpdlc`, `gps_l1` | Consume observations from navigation or surveillance systems with source and timestamp. |
| Amateur and software-defined radio · `radio` | 8 / 8 | `ax25`, `aprs`, `vrt` | A radio gateway can expose positions, frames or samples with call sign, channel and receive time. |
| Specialized radio data exchange and simulation · `fieldradio` | 8 / 10 | `s5066dts`, `dis`, `link16` | A domain plugin may consume exported events with the system's addressing and authorization. |
| Game networking · `gaming` | 12 / 12 | `quake`, `raknet`, `mcpe` | The game client or server owns entities, ticks and state synchronization. |

### Capture, formats and deferred entries

| Family (CSV key) | Current / all | Example keys | Handoff to rutis |
| --- | ---: | --- | --- |
| Capture, metadata and analysis tooling · `capture` | 137 / 142 | `frame`, `_ws.expert`, `pcapng.darwin`, `radiotap` | Use these as diagnostic and acceptance evidence. Capture time, interface and analyzer warnings do not supply a peer channel. |
| Encodings, files and payload formats · `format` | 30 / 31 | `cbor`, `json`, `protobuf`, `jpeg` | Adapters may use a codec. A format alone supplies no address, admission or lifecycle. |
| Legacy networking and protocol stacks · `legacy` | 63 / 66 | `ipx`, `ddp`, `clnp`, `x25` | Use an existing compatibility gateway when needed. Some filters remain available in current Wireshark. |
| Handoff to be established · `unresolved` | 4 / 5 | `gdt`, `iap`, `interlink`, `rlm` | Retain the original keys until there is a verifiable peer, handoff or deployment requirement. |

## Reproducing and updating the inventory

For each protocol paragraph in the official index, take the filter key before the colon, the link text and relative path, and all parenthesized version ranges. Preserve order and case and strip the trailing field count. Compare by filter key against the CSV. Expect zero missing, extra or duplicate keys. Resolve `dfref_path` against the index URL for the upstream link. On a release change, compare all intervals including discontinuous ranges, then review added or renamed keys. [Official index](https://www.wireshark.org/docs/dfref/)

To reproduce a checksum independent of page decoration, join `filter_id`, `dfref_path` and `versions` with single tabs for each row in CSV order. End every row with LF and encode as UTF-8. This projection has SHA-256 `b5e283cd0f1ac824633c7b5333a0a80a9db8c9e09d541ca7de9ae6bfd4f7fb25`.
