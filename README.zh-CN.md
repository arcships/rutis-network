# rutis-network

[English](README.md)

[rutis](https://github.com/arcships/rutis) 的链接层插件：连到另一台机器、设备或云的插件（SSH、Tailscale、Cloudflare Tunnel、TCP 与反向隧道、gRPC、移动设备、P2P），把每条连接作为一个会话交给 rutis。

rutis 自己只管会话这一侧：连接插件打开一条通道，交给服务 `rutis/sessions`（`rutis/sessions.open(far)`，Rust 里是 `Session::new(channel)`）。网络上的事——到达对面、准入与认证、分帧、心跳、重连、接替 listen 的位置——都是链接插件的，放在这里。见 [rutis：自建连接](https://github.com/arcships/rutis/blob/main/docs/guide/plugin-api.md#自建连接rutissessionsopen) 与 [连接节点](https://github.com/arcships/rutis/blob/main/docs/guide/nodes.md)。

插件基于 rutis 0.23.0（会话协议 5，插件 API 3）。两端宿主及其语言运行时需要一起升级。0.19.3 把服务改名为 `rutis/sessions`；0.20 改了卸载顺序，并让会话有自己的写线程；0.21 把收到的函数改为默认异步，并改了错误文本。

从单条连接扩展到多个节点或多个自治域时，可参考[发现、组网与跨域互联的服务边界研究](docs/research/network-groups.md)。
接入具体设备时，[网络栈调查](docs/research/network-stacks.md)列出物联网、工业和通用协议族在插件一侧实际提供的接口。比较 MQTT 等协议的订阅、确认、断线补收和重放，见[通信协议研究](docs/research/communication-protocols.md)。

[安全、稳定与可信研究](docs/research/network-assurance.md)沿设备、网关到云应用的路径分析准入、故障恢复和数据来源。

[Wireshark 协议族与支持建议](docs/research/wireshark-support.md)按 50 个分组逐项评估过滤器索引，并给出六项可验收的插件能力。

## 状态

[Go SSH 插件](docs/design/03-link-ssh.md) 支持通过 SSH 拨号、经 VPS 监听并接入 rutis 会话，以及反向转发到已有 TCP 服务。

[Cloudflare Tunnel Go 插件](docs/design/05-cloudflare-plugin-control.md)可连接两台 rutis 0.23 宿主。指南包含两端配置、可运行的 Go 服务示例和本地控制接口。

[开发指南](docs/guide/link-plugins.md)包含 Go TCP bridge、支持运行时管理的拨号与回环监听插件、用于互通验证的 TypeScript 参考实现、经过验证的 frp 和 OpenSSH 转发配置，以及可运行的 Rust 监听示例。其余工作按以下 issue 跟踪。

1. [#1](https://github.com/arcships/rutis-network/issues/1)：链接插件的契约——它做什么、不做什么；准入与身份；分帧、生命周期；一份参考 bridge。
2. [#2](https://github.com/arcships/rutis-network/issues/2)：各种链接方式的全景，以及决定优先级的场景。
3. [#3](https://github.com/arcships/rutis-network/issues/3)–[#9](https://github.com/arcships/rutis-network/issues/9)：各个链接（SSH、Tailscale、Cloudflare Tunnel、TCP bridge、gRPC、移动设备、iroh）。[#6](https://github.com/arcships/rutis-network/issues/6)（TCP bridge）是 TCP 类 adapter 共用的地基。
4. [#10](https://github.com/arcships/rutis-network/issues/10)：生态——各语言模板、可执行的一致性测试、适配器目录、CI 与许可证。

[Go 版 Tailscale 插件指南](docs/design/04-link-tailscale.md)给出按标签自动发现 worker 或指定单个节点的配置，支持系统守护进程和独立 tsnet 节点。

[Go 版 libp2p 插件指南](docs/design/09-p2p-plugins.md)给出拨号和监听配置，以及每条链路的管理方法。

这些 issue 的草稿在 [docs/issue-drafts/link-plugins](docs/issue-drafts/link-plugins/README.md)；设计稿在各 issue 的 draft PR 里（合并后落在 `docs/design/`）；给编程 agent 的指南在 [.agents/skills/rutis-link-plugin](.agents/skills/rutis-link-plugin/SKILL.md)。
