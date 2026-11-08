# rutis-network

[English](README.md)

[rutis](https://github.com/arcships/rutis) 的链接层插件：连到另一台机器、设备或云的插件（SSH、Tailscale、Cloudflare Tunnel、TCP 与反向隧道、gRPC、移动设备、P2P），把每条连接作为一个会话交给 rutis。

rutis 自己只管会话这一侧：连接插件打开一条通道，交给服务 `rutis/sessions`（`rutis/sessions.open(far)`，Rust 里是 `Session::new(channel)`）。网络上的事——到达对面、准入与认证、分帧、心跳、重连、接替 listen 的位置——都是链接插件的，放在这里。见 [rutis：自建连接](https://github.com/arcships/rutis/blob/main/docs/guide/plugin-api.md#自建连接rutissessionsopen) 与 [连接节点](https://github.com/arcships/rutis/blob/main/docs/guide/nodes.md)。

版本基线：rutis 0.21.0（会话协议 4，插件 API 3）。0.19.3 把服务改名为 `rutis/sessions`；0.20 改了卸载顺序，并让会话有自己的写线程；0.21 把收到的函数改为默认异步，并改了错误文本。

## 状态

规划中。工作按下面的顺序记在本仓库的 issue 里：

1. [#1](https://github.com/arcships/rutis-network/issues/1)：链接插件的契约——它做什么、不做什么；准入与身份；分帧、生命周期；一份参考 bridge。
2. [#2](https://github.com/arcships/rutis-network/issues/2)：各种链接方式的全景，以及决定优先级的场景。
3. [#3](https://github.com/arcships/rutis-network/issues/3)–[#9](https://github.com/arcships/rutis-network/issues/9)：各个链接（SSH、Tailscale、Cloudflare Tunnel、TCP bridge、gRPC、移动设备、iroh）。[#6](https://github.com/arcships/rutis-network/issues/6)（TCP bridge）是 TCP 类 adapter 共用的地基。
4. [#10](https://github.com/arcships/rutis-network/issues/10)：生态——各语言模板、可执行的一致性测试、适配器目录、CI 与许可证。

这些 issue 的草稿在 [docs/issue-drafts/link-plugins](docs/issue-drafts/link-plugins/README.md)；设计稿在 `docs/design/`；给编程 agent 的指南在 [.agents/skills/rutis-link-plugin](.agents/skills/rutis-link-plugin/SKILL.md)。
