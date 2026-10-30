# rutis-network

[中文](README.zh-CN.md)

Link plugins for [rutis](https://github.com/arcships/rutis): plugins that reach another machine, device or cloud (SSH, Tailscale, Cloudflare Tunnel, TCP and reverse tunnels, gRPC, mobile devices, P2P), and hand each connection to rutis as a session.

rutis itself knows only the session side: a connection plugin opens a channel and gives it to the `sessions` service (`sessions.open(far)`, or `Session::new(channel)` in Rust). Everything about the network — reaching the far end, admission and authentication, framing, heartbeats, reconnecting, taking over a listen slot — is the link plugin's job, and lives here. See [rutis: your own connection](https://github.com/arcships/rutis/blob/main/docs/guide/plugin-api.en.md#your-own-connection-sessionsopen) and [Connecting nodes](https://github.com/arcships/rutis/blob/main/docs/guide/nodes.en.md).

## Status

Planning. The work is tracked in this repository's issues, in this order:

1. [#1](https://github.com/arcships/rutis-network/issues/1) — the link plugin contract: what a link plugin does and does not do, templates in TypeScript, Python and Rust, a conformance suite.
2. [#2](https://github.com/arcships/rutis-network/issues/2) — the landscape of ways to link and the scenarios that set the priorities.
3. [#3](https://github.com/arcships/rutis-network/issues/3)–[#9](https://github.com/arcships/rutis-network/issues/9) — the links themselves (SSH, Tailscale, Cloudflare Tunnel, TCP, gRPC, mobile devices, iroh).
4. [#10](https://github.com/arcships/rutis-network/issues/10) — the ecosystem: templates, conformance, a catalogue of adapters.

The drafts these issues were written from are in [docs/issue-drafts/link-plugins](docs/issue-drafts/link-plugins/README.md); the guide for coding agents is [.agents/skills/rutis-link-plugin](.agents/skills/rutis-link-plugin/SKILL.md).
