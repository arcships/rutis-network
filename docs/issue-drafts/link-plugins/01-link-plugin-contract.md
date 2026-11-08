# feat(link): 定义链接层 link plugin 契约（adapter + bridge，任意语言）

状态：已发布为 [#1](https://github.com/arcships/rutis-network/issues/1)。优先级 P0。设计稿：[PR #11](https://github.com/arcships/rutis-network/pull/11)。

版本基线：rutis 0.21.0（2026-11-05），会话协议 4，插件 API 3。0.19.3 把服务 `sessions` 改名为 `rutis/sessions`；0.20 改了卸载顺序和会话写线程；0.21 把收到的函数改为默认异步，并改了错误文本。

## 问题

0.19 把链接层和 remote 插件拆开之后，链接层的扩展点是“写一个连接插件，拿到一条能收发消息的连接，交给 `rutis/sessions`”。但文档和示例只覆盖了 WebSocket / pipe / memory，没有把下面这些事写成一条可执行的契约：

- 链接层插件是普通插件，任何语言都能写；不是 Rust 专属 trait。
- 链接层负责到达、准入/认证、framing、心跳、重连、listen 顶替；不负责身份。
- 远端在 session `Hello` 里注册自己的 id；rutis 信任这个注册。
- session 不做认证，不碰 token、TLS、Tailscale、ACL；`rutis/sessions.open(far)` 就够，不加 peer 参数。
- adapter（网络工具相关）和 bridge（framing + `open` + 生命周期）怎么分。
- 0.20/0.21 之后：卸载是一次逆序栈、会话有自己的写线程、收到的函数默认异步、错误按 code 匹配。

没有这条契约，每个语言的 link plugin 都会重新决定认证、身份、framing、重连，最后又把职责耦合回 session。

## 范围

1. 写一份 `docs/guide/link-plugins.md`（`.en.md` 同步；放在本仓库，若 rutis 主仓库需要则在 rutis 侧补链接），给出：
   - 链接层与 session / remote 插件的职责表；
   - `rutis/sessions.open(far)` 的 `Far` / `Near` 契约，Rust 的 `Session::new(channel)` 与 `Session::handshake`；
   - 明确：认证/准入在链接层；身份来自远端 `Hello`；session 不认证、不拿 peer 参数；
   - 明确：adapter + bridge 的建议形态，adapter 可以是 CLI、SDK 或 sidecar；
   - 一条最小 TS / Rust 代码片段（Python 用同一 `Far` / `Near` 形状；完整模板归 #10），和一条 listen 侧示例；
   - 错误输出（按 code）、重连、close、顶替的约定；
   - 0.20 卸载顺序、0.21 异步回调、`SessionOptions::outbound_limit` 的注意事项。
2. 提供一个参考 bridge（先 Rust 或 TS），优先用 localhost TCP 或现有 WebSocket 验证，只依赖 `rutis/sessions`，不依赖核心新 API。各语言模板和一致性测试套件归 #10，本 issue 只定义契约和一份参考实现。
3. 给出一致性测试的**契约**（测试套件在 #10）：
   - 消息边界、顺序、背压、异步 `send`、close；
   - 远端 `Hello` id 命名 `<id>:<plugin>`；
   - listen 顶替（Rust）与非 Rust 的限制；
   - 认证在 `open` 之前，session 不感知。
4. 在 rutis 仓库 `docs/guide/plugin-api.md` 和 `docs/guide/nodes.md` 的“自建连接”一节补上契约链接；不新增核心 trait，不改 `open(far)` 签名。

## 关键决策

- **listen 顶替**：Rust 用 `Session::handshake` 拿 `Greeted::id()`，先卸旧 session、再挂新 session。0.21.0 的 `open(far)` 只返回 `{ receive, end }`，不暴露远端 id；因此非 Rust 的 listen bridge 不能按 `Hello` id 顶替。契约要求：Rust 实现顶替；其它语言一条连接一个 session，并写明“同 id 的多条连接并存”。禁止解析 session 协议，也禁止拿 SSH 用户 / tailnet 节点 / token 代替 `Hello` id。
- **认证与身份分开**：adapter 做准入；`Hello` 的 `endpoint` 是身份。`open` 不加 `peer` / `token` / `credential` / `identity`。
- **bridge 不做第二层 session**：只 framing、backpressure、close/EOF 映射和生命周期。
- **WebSocket 不是新 link plugin**：核心已经有 `websocket::dial` / `websocket::listen`，`rutis-host/listen` 也用它；第一波只把现有 WebSocket 连接写成参考示例，不新增 `link/websocket`。纯 HTTP 请求/响应不是 link，HTTP 只作为 WebSocket 升级或 gRPC 的承载。
- **错误按 code**：0.21 的错误文本不是契约；`bad-value` 的 `data.reason`、`bad-result` 的 `data.service` / `method` / `expected` 才是。

## 非目标

- 不重新引入 0.19 删除的 `Transport` trait、`ChannelInfo.peer`、`Identity` / `Admission` 一族。
- 不给 `open(far)` 加 peer、token、credential 参数。
- 不让 session 层做认证或身份绑定。
- 不把链接层插件限制在 Rust。
- 不在本 issue 里实现各语言模板、适配器目录或 CI（#10）。

## 验收

- [ ] 文档写清“链接层认证 vs 远端注册身份”，并用代码/文档引用证明：`Channel` 不知道对面是谁，远端在握手时报 id。
- [ ] #6 的 TS 参考 bridge 跑通 `open(far)`；Rust 的 `Session::new` / `Session::handshake` 片段也能跑；示例的认证在 `open` 之前完成，session 代码里没有 token / TLS / Tailscale。
- [ ] listen 侧：Rust 示例能接受多条连接、每条一个 session，并按远端 `Hello` id 顶替；非 Rust 示例写明“不做同 id 顶替”的限制，并说明原因。
- [ ] 文档覆盖 0.20 卸载顺序（cleanup 只注册一次、不在 cleanup 里注册/等待 cleanup）、0.21 异步 `send` 和错误 code。
- [ ] 一致性测试契约覆盖消息边界、顺序、背压、异步 `send`、close、远端 id；套件本身在 #10 实现。
- [ ] `cargo test` / 语言测试按仓库现有流程通过；文档链接检查在全部设计 PR 合并后跑（设计稿互相引用）。

## 关联

- 设计稿：[PR #11](https://github.com/arcships/rutis-network/pull/11)
- Agent 指南：[.agents/skills/rutis-link-plugin/SKILL.md](../../../.agents/skills/rutis-link-plugin/SKILL.md)
- [远程连接：只留一个会话插件](https://github.com/arcships/rutis/blob/main/docs/design-remote-session-2026-10-28.md) §2、§5
- [协议与 Channel 解耦](https://github.com/arcships/rutis/blob/main/docs/design-protocol-channel-decoupling-2026-10-03.md)
- `crates/rutis-bridge/src/channel/mod.rs`
- `crates/rutis-bridge/src/session/plugin/mod.rs`
- `crates/rutis-bridge/src/session/plugin/open.rs`
- [插件 API：自建连接](https://github.com/arcships/rutis/blob/main/docs/guide/plugin-api.md#自建连接rutissessionsopen)
- [连接节点：自建连接](https://github.com/arcships/rutis/blob/main/docs/guide/nodes.md#自建连接)
- [0.19.3 发布说明（服务改名）](https://github.com/arcships/rutis/blob/main/docs/releases/0.19.3.md)
- [0.20 迁移（卸载顺序）](https://github.com/arcships/rutis/blob/main/docs/migration-0.20.md)
- [0.21 迁移（异步回调、错误 code）](https://github.com/arcships/rutis/blob/main/docs/migration-0.21.md)
