# rutis-network

[中文](README.zh-CN.md)

Link plugins for [rutis](https://github.com/arcships/rutis): plugins that reach another machine, device or cloud (SSH, Tailscale, Cloudflare Tunnel, TCP and reverse tunnels, gRPC, mobile devices, P2P), and hand each connection to rutis as a session.

rutis itself knows only the session side: a connection plugin opens a channel and gives it to the `rutis/sessions` service (`rutis/sessions.open(far)`, or `Session::new(channel)` in Rust). Everything about the network — reaching the far end, admission and authentication, framing, heartbeats, reconnecting, taking over a listen slot — is the link plugin's job, and lives here. See [rutis: your own connection](https://github.com/arcships/rutis/blob/main/docs/guide/plugin-api.en.md#your-own-connection-rutissessionsopen) and [Connecting nodes](https://github.com/arcships/rutis/blob/main/docs/guide/nodes.en.md).

The plugins target rutis 0.23.0 (session protocol 5, plugin API 3). Upgrade both hosts and their language runtimes together. 0.19.3 renamed the service to `rutis/sessions`; 0.20 changed unload order and gave a session its own writer thread; 0.21 made received functions asynchronous and changed error texts.

For networks that span several nodes or administrative domains, see [the research on discovery, network formation and service boundaries](docs/research/network-groups.en.md).
For device integration, the [network stack survey](docs/research/network-stacks.en.md) identifies the interfaces that IoT, industrial and general-purpose protocols expose to a plugin. The [communication protocol survey](docs/research/communication-protocols.en.md) compares subscriptions, acknowledgments, offline delivery and replay, with MQTT as a worked example.

[Security, reliability and trustworthiness](docs/research/network-assurance.en.md) follows a device-to-cloud path through admission, failure recovery and data provenance.

The [Wireshark protocol family survey](docs/research/wireshark-support.en.md) evaluates the display filter index across 50 groups and proposes four further plugin capabilities to validate.

## Development checks

From the repository root, use Git, Make, Go with toolchain selection enabled, and a supported Node.js version (`^22.18.0 || ^24.11.0 || >=26.0.0`). Run `make setup` after checkout or a lockfile change to install the pinned formatter and the TypeScript packages' development dependencies.

| Command | What it runs |
| --- | --- |
| `make fmt` | Format tracked Go files with gofmt and tracked `.ts`, `.mts`, and `.mjs` files with Prettier. |
| `make fmt-check` | List files that need formatting and exit with an error if any do. Leaves source files untouched. |
| `make test` | Run Go short tests in each module and Vitest unit tests in each TypeScript package. |
| `make check` | Check formatting, run Go vet and strict TypeScript checks, then run the fast tests. |

The root commands and Linux PR jobs use Go 1.27.4 from [.go-version](.go-version), including its gofmt. To use that toolchain for standalone commands in the same shell, run `export GOTOOLCHAIN="go$(cat .go-version)"` from the repository root. Each module's `go.mod` declares its minimum Go version for consumers. Checks for modules that depend on the TCP bridge use the checked-out bridge in a temporary Go workspace. For a single job, run e.g. `make go-check GO_MODULES=link/ssh` or `make typecheck ts-test TS_PACKAGES=link/webrtc/web`. The TCP and WebRTC packages also provide `npm run test:integration` and `npm run test:delivery` for real hosts, browsers and package installations.

## Status

The Go DNS-SD/mDNS plugin discovers and advertises local ingress endpoints on a selected interface. A consuming plugin can read the current candidates and wait for changes. [Configuration and Go API](discovery/mdns/README.md)

Browsers can connect to each other or to a native rutis host over a WebRTC DataChannel, directly or via authenticated TURN. [WebRTC setup and example](docs/design/08-link-webrtc.en.md)

The [Go SSH plugins](docs/design/03-link-ssh.en.md) dial rutis peers, accept sessions through a VPS, and reverse-forward streams to an existing TCP service.

The [Cloudflare Tunnel Go plugin](docs/design/05-cloudflare-plugin-control.en.md) connects two rutis 0.23 hosts. The guide includes both configurations, a working Go service example and the local control API.

The [connection plugin guide](docs/guide/link-plugins.en.md) includes the Go TCP bridge and its managed dial and loopback listen plugins, the TypeScript interoperability reference, tested frp and OpenSSH forwarding configurations, and a runnable Rust listener example. The roadmap is tracked in these issues.

1. [#1](https://github.com/arcships/rutis-network/issues/1) — the link plugin contract: what a link plugin does and does not do, admission vs identity, framing, lifecycle, and one reference bridge.
2. [#2](https://github.com/arcships/rutis-network/issues/2) — the landscape of ways to link and the scenarios that set the priorities.
3. [#3](https://github.com/arcships/rutis-network/issues/3)–[#9](https://github.com/arcships/rutis-network/issues/9) — the links themselves (SSH, Tailscale, Cloudflare Tunnel, TCP bridge, gRPC, mobile devices, iroh). [#6](https://github.com/arcships/rutis-network/issues/6), the TCP bridge, is the foundation the TCP-shaped adapters share.
4. [#10](https://github.com/arcships/rutis-network/issues/10) — the ecosystem: language templates, the executable conformance suite, a catalogue of adapters, CI and the license.

The [Go Tailscale plugin guide](docs/design/04-link-tailscale.en.md) covers tag-based worker discovery and single-node connections using the system daemon or an embedded tsnet node.

The [Go libp2p plugin guide](docs/design/09-p2p-plugins.en.md) gives the dial and listen configurations and the control methods for a single link.

The drafts these issues were written from are in [docs/issue-drafts/link-plugins](docs/issue-drafts/link-plugins/README.md); the design drafts are in the draft PRs for each issue (they land in `docs/design/` when merged); the guide for coding agents is [.agents/skills/rutis-link-plugin](.agents/skills/rutis-link-plugin/SKILL.md).
