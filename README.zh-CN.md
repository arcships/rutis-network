# rutis-network

[English](README.md)

[rutis](https://github.com/arcships/rutis) 的链接层插件：连到另一台机器、设备或云的插件（SSH、Tailscale、Cloudflare Tunnel、TCP 与反向隧道、gRPC、移动设备、P2P），把每条连接作为一个会话交给 rutis。

rutis 自己只管会话这一侧：连接插件打开一条通道，交给服务 `sessions`（`sessions.open(far)`，Rust 里是 `Session::new(channel)`）。网络上的事——到达对面、准入与认证、分帧、心跳、重连、接替 listen 的位置——都是链接插件的，放在这里。见 [rutis：自建连接](https://github.com/arcships/rutis/blob/main/docs/guide/plugin-api.md#自建连接sessionsopen) 与 [连接节点](https://github.com/arcships/rutis/blob/main/docs/guide/nodes.md)。

## 状态

规划中。工作按下面的顺序记在本仓库的 issue 里：

1. [#1](https://github.com/arcships/rutis-network/issues/1)：链接插件的契约——它做什么、不做什么；TypeScript、Python、Rust 的模板；一致性测试。
2. [#2](https://github.com/arcships/rutis-network/issues/2)：各种链接方式的全景，以及决定优先级的场景。
3. [#3](https://github.com/arcships/rutis-network/issues/3)–[#9](https://github.com/arcships/rutis-network/issues/9)：各个链接（SSH、Tailscale、Cloudflare Tunnel、TCP、gRPC、移动设备、iroh）。
4. [#10](https://github.com/arcships/rutis-network/issues/10)：生态——模板、一致性测试、适配器目录。

这些 issue 的草稿在 [docs/issue-drafts/link-plugins](docs/issue-drafts/link-plugins/README.md)；给编程 agent 的指南在 [.agents/skills/rutis-link-plugin](.agents/skills/rutis-link-plugin/SKILL.md)。
