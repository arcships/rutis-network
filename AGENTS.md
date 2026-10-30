# AGENTS

Task guides for coding agents live in `.agents/skills/<name>/`. Each skill is self-contained (`SKILL.md` + `references/`).

| Task | Guide |
| --- | --- |
| Extend the link layer (a connection plugin / adapter for SSH, Tailscale, Cloudflare, gRPC, ADB, iroh, …) | [.agents/skills/rutis-link-plugin/SKILL.md](.agents/skills/rutis-link-plugin/SKILL.md) |

The rutis side of a link (sessions, `sessions.open`, `Session::new`) is documented in [rutis](https://github.com/arcships/rutis); its design principles are in [design-philosophy.en.md](https://github.com/arcships/rutis/blob/main/docs/design-philosophy.en.md) §7. Documentation is kept in two languages (`.md` and `.zh-CN.md` / `.en.md`).
