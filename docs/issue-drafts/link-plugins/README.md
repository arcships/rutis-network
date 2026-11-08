# 链接层 link plugins：issue 草案

状态：已发布为 [#1](https://github.com/arcships/rutis-network/issues/1)–[#10](https://github.com/arcships/rutis-network/issues/10)。本地保留草案用于评审和追溯，**本地草案是 issue body 的来源**。Agent 指南：[.agents/skills/rutis-link-plugin/SKILL.md](../../../.agents/skills/rutis-link-plugin/SKILL.md)。

版本基线：**rutis 0.21.0**（2026-11-05），会话协议 4，`rutis.json` format 1，插件 API 3。0.19.3 把服务 `sessions` 改名为 `rutis/sessions`；0.20 改了卸载顺序与会话写线程；0.21 把收到的函数改为默认异步，并改了错误文本（按 code 匹配）。这些变化已经写进草案和 skill。

这组 issue 承载“链接层 adapter / link plugin”的契约、跨网络方案分析、场景优先级和后续拓展。设计依据：

- [远程连接：只留一个会话插件](https://github.com/arcships/rutis/blob/main/docs/design-remote-session-2026-10-28.md)
- [协议与 Channel 解耦](https://github.com/arcships/rutis/blob/main/docs/design-protocol-channel-decoupling-2026-10-03.md)
- [插件 API：自建连接](https://github.com/arcships/rutis/blob/main/docs/guide/plugin-api.md#自建连接rutissessionsopen)
- [连接节点](https://github.com/arcships/rutis/blob/main/docs/guide/nodes.md)

## 已对齐的结论

- 链接层是**普通插件，任何语言都能写**；不是 Rust trait，也不是 underlay。
- 链接层负责：到达、准入/认证、framing、心跳、重连、listen 顶替。
- 链接层**不负责身份**：远端在 session `Hello` 里注册自己的 id，rutis 信任这个注册。
- rutis 完全信任链接层放进来的连接；session 不做认证，不碰 token、TLS、Tailscale、ACL。
- `rutis/sessions.open(far)` 足够：不加 peer 参数，session 生命周期只绑 channel。
- 建议形态：**adapter + bridge**。adapter 是网络工具相关的一层；bridge 是 framing + `rutis/sessions.open(far)` + 生命周期，每种语言一份。
- listen 顶替需要握手后的远端 id。Rust 用 `Session::handshake` 拿 `Greeted::id()`；0.21.0 的 `open(far)` 只返回 `{ receive, end }`，其它语言拿不到 id，因此**非 Rust 的 listen 顶替暂不可实现**。契约要求：Rust 实现顶替；其它语言接受一条连接一个 session，并写明限制，不得解析 session `Hello`，也不得拿 adapter 的准入身份代替。
- 错误按 `code` 匹配，不按文本匹配（0.21）。

## Issue 列表

| 顺序 | Issue | 草案 | 设计稿 | 优先级 | 依赖 |
| --- | --- | --- | --- | --- | --- |
| 1 | [#1 链接层 link plugin 契约（adapter + bridge，任意语言）](https://github.com/arcships/rutis-network/issues/1) | [01](01-link-plugin-contract.md) | [01](../../design/01-link-plugin-contract.md) | P0 | 无 |
| 2 | [#2 跨网络 link 方案全景、场景与优先级](https://github.com/arcships/rutis-network/issues/2) | [02](02-link-landscape-and-priorities.md) | [02](../../design/02-link-landscape.md) | P0 | 1 |
| 3 | [#3 `link/ssh`：用 SSH 做链接层](https://github.com/arcships/rutis-network/issues/3) | [03](03-link-ssh.md) | [03](../../design/03-link-ssh.md) | P1 | 1、6 |
| 4 | [#4 `link/tailscale`：tailnet TCP 做链接层](https://github.com/arcships/rutis-network/issues/4) | [04](04-link-tailscale.md) | [04](../../design/04-link-tailscale.md) | P1 | 1、6 |
| 5 | [#5 `link/cloudflared`：Cloudflare Tunnel 做链接层](https://github.com/arcships/rutis-network/issues/5) | [05](05-link-cloudflared.md) | [05](../../design/05-link-cloudflared.md) | P1 | 1、6 |
| 6 | [#6 `link/tcp` 与反向隧道 wrapper（frp/rathole/ngrok/SSH -R）](https://github.com/arcships/rutis-network/issues/6) | [06](06-link-tcp-and-reverse-tunnels.md) | [06](../../design/06-link-tcp.md) | **P0**（所有 TCP 类 adapter 的地基） | 1 |
| 7 | [#7 `link/grpc`：云控制面 / relay / 分布式云](https://github.com/arcships/rutis-network/issues/7) | [07](07-link-grpc-cloud.md) | [07](../../design/07-link-grpc.md) | P1（云业务）/ P2（其他） | 1 |
| 8 | [#8 移动设备链路与远控服务（ADB/usbmuxd/WebRTC + screen/input）](https://github.com/arcships/rutis-network/issues/8) | [08](08-mobile-and-remote-control.md) | [08](../../design/08-mobile.md) | P2（8a）/ P3（8b） | 1、6 |
| 9 | [#9 `link/iroh`：Rust P2P / relay 原型](https://github.com/arcships/rutis-network/issues/9) | [09](09-link-iroh-and-p2p.md) | [09](../../design/09-link-iroh.md) | P3 | 1 |
| 10 | [#10 链接层插件生态：模板、一致性测试、适配器目录](https://github.com/arcships/rutis-network/issues/10) | [10](10-link-ecosystem.md) | [10](../../design/10-link-ecosystem.md) | P2 | 1、3–6 |

拆分说明：

- **#1 与 #10 不再重叠**：#1 只负责契约 + 一个参考 bridge + 一致性测试的契约；各语言模板、适配器目录和 CI 归 #10。
- **#8 分两层**：8a 是设备链路（ADB、usbmuxd、WebRTC），8b 是远控服务（screen/input/audio/clipboard）。远控是应用服务，不是 link。
- **#6 提到 P0**：#3 的 forward 模式、#4 的 framing、#5 的 access 模式、#8 的 ADB/usbmuxd 都落在它的 bridge 上。

## 设计稿与 PR

每个 issue 有一份设计稿，放在 `docs/design/`（中英各一份），并在对应 issue 里链接到 draft PR。设计稿记录：问题、决策、接口/帧格式、准入、生命周期、错误、测试、非目标和开放问题。设计稿是评审材料，不是实现；实现仍按 issue 的验收走。

## 发布状态

已用 `gh issue create` 发布。发布时正文里的旧编号（#949–#958）和指向 `arcships/rutis/docs/issue-drafts/` 的链接已经修正，现在与本地草案一致（差异只允许出现在 issue 专用的绝对链接上）。新增或修改时，先改本地草案，再用 `gh issue edit <number> --body-file <file>` 同步；同步后复核正文里的编号和链接。
