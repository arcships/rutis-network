# Cross-network link landscape, scenarios and priorities: design

Status: draft. Date: 2026-11-08. Baseline: rutis 0.21.0.
Issue: [#2](https://github.com/arcships/rutis-network/issues/2).

## 1. Purpose

Decide what to build first, not list protocols. The scenario decides: where the plugin runs, whether the host is behind NAT, whether the user can install a client, and whether this is a person, a team or a vendor.

## 2. Candidate classes

| Class | Examples | What it gives | Link plugin shape |
| --- | --- | --- | --- |
| Direct stream | TCP, Unix socket, pipe, WebSocket, QUIC, WebTransport | bytes or messages | adapter + bridge |
| Overlay | Tailscale/Headscale, ZeroTier, Nebula, NetBird, WireGuard, EasyTier, OpenZiti, Twingate, Netmaker, Innernet, Firezone, Yggdrasil, n2n, tinc, Cloudflare WARP | IP/TCP | adapter dials TCP; overlay admits |
| Reverse tunnel | Cloudflare Tunnel, ngrok, frp, rathole, SSH `-R`, bore, chisel, sish, zrok, Pangolin, Azure Relay, Tailscale Funnel | local/public TCP | adapter dials TCP |
| P2P | iroh, libp2p, WebRTC, OpenZiti SDK, Veilid, Hyperswarm | QUIC stream / data channel | library + framing |
| Cloud link | gRPC control plane/relay, cloud runtime, K8s/service mesh, distributed cloud | bidi stream | `link/grpc` |
| Mobile device | ADB `forward`/`reverse`, usbmuxd/`iproxy` | local TCP | `link/tcp` |
| Remote control | RDP, VNC, SPICE, Citrix, PCoIP, TeamViewer, AnyDesk, RustDesk, Sunshine/Moonlight | screen/input/audio | **application service, not a link** |
| Message bus | MQTT, NATS, Redis, Kafka, RabbitMQ, PubNub, Ably, AWS IoT, Azure Service Bus, Google Pub/Sub | messages/topics | request/reply + session mapping, not connection semantics |
| Local/physical | pipe, Unix socket, fd, Bluetooth, serial, USB, NFC, LoRa | bytes/messages | OS API + framing |

## 3. Scenario table

| Scenario | How the user uses it | First choice | Friction |
| --- | --- | --- | --- |
| Personal GPU / remote machine | laptop host runs a Python plugin on a GPU box | SSH / Tailscale | SSH is already there but tunnels/reconnect are manual; Tailscale needs an account/client |
| Phone / device app | device plugins provide camera/sensors/notifications; host on a laptop/server | Cloudflare Tunnel / Tailscale / iroh | Cloudflare has public ingress; Tailscale takes a VPN slot; iroh needs embedding |
| Home lab / self-hosted | NAS, Pi, old PC, a plugin on each | Tailscale / Headscale / frp | Tailscale is easiest; Headscale is self-hosted; frp needs a VPS |
| Team / enterprise | VPC/office rutis, SSO/ACL/audit, no public ports | Tailscale / Cloudflare Access / NetBird / OpenZiti | procurement, compliance, vendor |
| Demo / tutorial | phone connects to a laptop in five minutes | Cloudflare quick tunnel / ngrok / SSH | temporary URL, public exposure, rate limits |
| China / VPS | CGNAT home network, one public VPS | frp / rathole / SSH `-R` | VPS, TLS, operations; no mesh |
| Deployment platform | rutis in K8s/Docker/Fly.io/Railway; local or another service connects | platform network integration | platform-specific; ephemeral ports |
| Distributed cloud | hosts connect to a cloud control plane/relay/runtime | gRPC | HTTP/2 proxies, long-lived streams, session affinity |
| Remote control | screen/input/audio of a device | link + remote-control plugin | RDP/VNC are application protocols, not links |
| IoT / outbound-only | device can only connect out; cloud calls its plugins | MQTT / NATS / AWS IoT / Azure Relay | not connection semantics; request/reply and session mapping |
| Browser | web page uses a local device's plugins | WebRTC / WebTransport / WebSocket | signaling, TURN, permissions |
| Local / offline | same machine/process | pipe / memory / Unix socket | none |

## 4. Priorities

#6's `link/tcp` bridge is the **P0 foundation**: SSH forward, Tailscale, Cloudflare access, ADB, usbmuxd and frp/rathole all end in TCP. Without it each adapter writes framing again.

| Tier | Content | Why |
| --- | --- | --- |
| P0 | #1 contract, #2 this analysis, #6 TCP bridge | Settle the boundary and the shared bridge first; otherwise duplication and coupling |
| P1 | #3 SSH, #4 Tailscale, #5 Cloudflare Tunnel | Cover "I already have SSH", "private network/NAT/ACL", "no client install/public ingress" |
| P2 | #7 gRPC (cloud business only; otherwise P3), #8 mobile links, #10 ecosystem | Depends on a cloud/device business; ecosystem waits for T1 |
| P3 | #8b remote-control services, #9 iroh, message buses, enterprise zero-trust | Application layer or early technology; does not block T1 |

## 5. What each candidate gives

- **SSH**: bytes (stdio) or local TCP (`-L`/`-R`); host key + user key/agent admits; the bridge frames.
- **Tailscale**: tailnet TCP; tailnet ACL/device identity admits; the bridge frames. Do not describe it as "WebSocket over Tailscale".
- **Cloudflare Tunnel**: local TCP (`access tcp`) or public WSS; Access service token/identity admits; document public-ingress risk.
- **frp/rathole/ngrok/SSH -R**: local or public TCP; tunnel token/TLS/SSH admits; the bridge is unchanged.
- **gRPC**: bidi messages; mTLS/OAuth/JWT/API key admits; a cloud link, not a device link.
- **ADB/usbmuxd**: local TCP; device authorization/pairing admits; the bridge is unchanged.
- **WebRTC/iroh/libp2p**: data channel/QUIC stream; DTLS/endpoint key/app token admits; prototype first, no production promise.
- **Remote-control protocols**: screen/input/audio, an application service; the link only carries the channel.
- **Message buses**: messages/topics, not connection semantics; request/reply and session mapping, not the link layer.

## 6. Validation of existing issues

- #3 SSH forward mode now depends on #6.
- #4 Tailscale framing lands in #6; dependency updated.
- #5 Cloudflare access mode lands in #6; dependency updated.
- #8 ADB/usbmuxd/WebRTC land in #6; remote-control service split as 8b.
- #7 priority changed to "cloud business P1, otherwise P2/P3".
- #10 no longer overlaps #1: templates, catalogue and CI are #10.

## 7. What not to do

- Do not turn RDP/VNC/SPICE into a generic link.
- Do not treat a message bus as connection semantics.
- Do not use gRPC for NAT traversal/device interconnect.
- Do not make Tailscale/Cloudflare a core dependency.
- Do not rank by protocol popularity; do not promise iroh production.

## 8. Constraints from 0.21

- Non-Rust listen replacement is impossible (`open` does not expose the far id); use Rust for a listener that must replace.
- Error texts are not stable; docs and tests match codes.
- gRPC's default 4 MiB limit differs from rutis's 16 MiB; raise it or split.
- Public ingress (Cloudflare public, ngrok) needs an explicit security boundary.

## 9. Open questions

- O1: Is a separate Headscale/NetBird self-hosted control-plane adapter worth it, or only compatibility notes?
- O2: DERP availability in China; should frp/rathole be the default there?
- O3: Which link should WebRTC signaling reuse by default (SSH/Tailscale/Cloudflare/gRPC)?

## Links

- Issue [#2](https://github.com/arcships/rutis-network/issues/2)
- [Contract design](01-link-plugin-contract.en.md)
- [TCP bridge design](06-link-tcp.en.md)
- [SKILL.md](../../.agents/skills/rutis-link-plugin/SKILL.md)
