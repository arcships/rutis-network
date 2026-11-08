# 链接层 link plugin 契约：设计稿

状态：草案。日期：2026-11-08。基线：rutis 0.21.0（2026-11-05），会话协议 4，`rutis.json` format 1，插件 API 3。
关联 issue：[#1](https://github.com/arcships/rutis-network/issues/1)。
配套：[SKILL.md](../../.agents/skills/rutis-link-plugin/SKILL.md)、[link-plugin-api.md](../../.agents/skills/rutis-link-plugin/references/link-plugin-api.md)。

## 1. 问题

0.19 把链接层从 session 核心里拆出去之后，扩展点变成“普通插件拿到一条能收发消息的连接，交给 `rutis/sessions`”。但文档只覆盖 WebSocket / pipe / memory，没有回答：

- 哪些事归链接层，哪些归 session；
- 准入和身份怎么分；
- 字节流怎么变成消息；
- 重连、close、listen 顶替归谁；
- 0.19.3 / 0.20 / 0.21 的变化怎么落到 bridge 上。

没有契约，每个语言的 link plugin 会各自决定这些事，最后把认证、身份、重连又耦合回 session。

## 2. 决策

### D1 链接层是普通插件，不是核心 trait

- 任意语言，只依赖 `rutis/sessions`（Rust 直接用 `Session::new`）。
- 不新增 `Transport`、`LinkPlugin`、`ChannelInfo`；不把 vendor 变成核心依赖。
- 0.19 删除的 `Identity` / `Admission` 不回来：认证在链接层。

### D2 职责边界

| 层 | 负责 | 不负责 |
| --- | --- | --- |
| 链接层（adapter + bridge） | 到达、准入/认证、framing、心跳、重连、listen 顶替、背压 | 远端身份、session 协议、插件命名 |
| session（`rutis/sessions` / `rutis-bridge/session`） | 握手、远端插件挂载、服务/事件、生命周期 | 认证、token、TLS、ACL、传输细节 |
| 远端 | 在 `Hello` 里报自己的 id；注册自己的插件 | 决定本端怎么连它 |
| 应用插件 | 屏幕/输入、业务服务 | 传输与准入 |

### D3 adapter + bridge

```text
link plugin
  ├─ adapter：SSH / Tailscale / Cloudflare / gRPC / ADB / WebRTC / iroh / frp
  │    到达 + 准入，给字节流或消息流
  └─ bridge：framing + rutis/sessions.open(far) + 生命周期
```

- adapter 可以是 CLI（`ssh`、`cloudflared`、`adb`、`iproxy`）、SDK 或 sidecar。
- bridge 每种语言一份；adapter 不重复 framing、close、背压。
- 一个 channel = 一个 session；一个 adapter 有多条连接就开多个 session。

### D4 准入与身份分开

- 准入发生在 `open` / `Session::new` **之前**。失败就关闭连接，不创建 session。
- 身份来自 session `Hello` 的 `endpoint`；rutis 信任它，用它命名 `<id>:<plugin>`。
- `open(far)` 不加 `peer` / `token` / `credential` / `identity`。
- 不把 SSH 用户、tailnet 节点、Access token 传给 session，也不拿它们代替 `Hello` id。

### D5 字节流归一化为消息

- 长度前缀优先：4 字节大端长度 + 正好这么多字节；超长在分配前拒绝。
- 最大消息 16 MiB，对齐 WebSocket 默认；超长干净关闭。
- 保持顺序与背压；bridge 的发送队列有界。
- close / 半关闭映射到 `near.end({kind:'done'})`；传输只关一次。
- 不解析、不改写 session 协议；bridge 不是第二层 session。

### D6 生命周期

- `Channel` 关闭 → session 卸载；session 卸载 → 关闭 channel。
- 重连/退避在链接层；session 从不重连。
- 0.20 起卸载是一条逆序栈：adapter 清理只注册一次，不在 cleanup 里注册/等待 cleanup。
- 0.20 起 session 有自己的写线程，出站队列默认无上限。Rust 用 `Session::options(SessionOptions { outbound_limit: Some(bytes) })`；非 Rust 的 `open(far)` 没有这个参数，bridge 自己的队列**不能**限制 session 的队列——这是 O2，生产用非 Rust link 前必须解决。
- 0.21 起收到的函数默认异步：`send` 可以是 async，session 会 await，传输写阻塞由此回压。
- 心跳是 adapter 的事：SSH keepalive、WebSocket ping、gRPC keepalive、Tailscale 自己的心跳；bridge 不发明通用心跳帧。心跳失败就关闭连接并映射为 `near.end`。

### D7 listen 顶替

- Rust：`Session::handshake(&ctx, channel).await` → `Greeted::id()`；先卸同 id 旧 session，再用 `ctx.plugin(greeted)` 挂新的；旧 channel 以 `Close::Replaced`（4002）关闭。
- 非 Rust：0.21.0 的 `open(far)` 只返回 `{ receive, end }`，没有远端 id，做不了同 id 顶替。也**不要**说“同 id 的多条连接并存”：`session_key(row)` 是先到先得（`crates/rutis-bridge/src/session/plugin/mod.rs`），同一个 row 下第二个 session 不再注册 handle，`<row>:<plugin>` 只会找到第一个。需要顶替的监听侧必须用 Rust；O1 是阻塞项，不是可选优化。
- 禁止在 bridge 里解析 `Hello` 来拿 id；禁止用 adapter 的准入身份代替。

### D8 错误

- 0.21 改了几乎所有错误文本；按 `code` 和 `data` 匹配。
- `bad-value` 的 `data.reason`：`non-finite`、`cycle`、`too-deep`、`type`、`tag`、`limit`。
- `bad-result`：`data.service`、`data.method`、`data.expected`。
- 链接层错误要说清行/连接/adapter 地址，不带 token。

### D9 参考实现与一致性契约

- 参考 bridge 定为 #6 的 TS `link/tcp`；Rust 片段只用来演示 `Session::handshake` 的 listen 顶替。各语言模板归 #10。
- 一致性契约：边界、顺序、最大消息（adapter/bridge 自己的策略，不是 `Channel` 的契约）、异步 send、背压、close；session **没有 half-close**，只有 `near.end`，EOF 映射为 `done` 并关闭双向；远端 id 命名、准入失败不建 session、卸载关闭连接、Rust listen 顶替。

### D10 localhost TCP / WebSocket 是第一波，HTTP 不是

- 核心已有 `websocket::dial` / `websocket::listen`，`rutis-host/listen` 也用它；不新增 `link/websocket`。
- 参考 bridge 先用 localhost TCP（#6）或现有 WebSocket 验证；两者都只绑 `127.0.0.1`，回环也要随机端口/一次性 token 准入。
- 纯 HTTP 请求/响应没有双向长连接和消息边界，不是 link；HTTP 只作为 WebSocket 升级或 gRPC 的承载。

### D11 组合靠 row + session + 服务名，不做跨 link 中心

- 链接插件的公开面只有两样：一行配置（`rutis.json` 的 row）和它挂上的 session。
- 业务插件不认识链接插件。对面要跑什么插件由 `<link row>:<plugin>` 行决定；服务起来后，业务插件按服务名 `inject` / `use`。
- 不设全局 `links` manager、全局 `connect` 服务、跨 link 注册表或中心凭据库。每个 row 自己管准入、凭据、重试、listen 和子进程。
- 动态增删用 `rutis/loader` / `rutis.json`，这是部署/运维层，不是链接层的运行时 API。优先常驻 + 重连。
- 每条 link 可以有可选的 `status` / `doctor` / CLI，作为该 link 自己的外部工具；不做跨 link 的中心管理工具。
- 链接层指南放在**本仓库**的 `docs/guide/link-plugins.md`；rutis 主仓库只在 `docs/guide/plugin-api.md` / `nodes.md` 的“自建连接”里加链接。

```text
业务 plugin ── use ──> 远端服务
                         ▲
                    session（rutis/sessions）
                         ▲
link row（link/ssh 等）── 传输/准入/framing/重连 ──> 对面
```

## 3. 接口

Rust：

```rust
let channel = adapter.connect(&config).await?;   // 准入已完成
let session = ctx.plugin(Session::new(channel));  // session 生命周期 == channel 生命周期
```

TS：

```ts
const near = await ctx.use('rutis/sessions').open({
  send: message => connection.send(message), // 0.21：可以是 async
  close: why => connection.close(why),
})
connection.onMessage(message => near.receive(message))
connection.onClose(() => near.end({ kind: 'done' }))
```

## 4. 测试

- 假 `Far` / `Near`：边界、顺序、最大消息、close、背压。
- 异步 `send` 返回 Promise，session await 它；慢发送不撑大 bridge 队列。
- 远端 `Hello` id → `<id>:<plugin>`。
- listen：Rust 多连接 + 同 id 顶替；非 Rust 多连接并存。
- 准入失败在 `open` 之前；没有 session 被创建。
- session 卸载后 adapter 连接关闭。
- 输出里没有 token/证书。

## 5. 非目标

- 不新增核心 trait / API。
- 不给 `open` 加 peer/token/credential。
- 不让 session 做认证。
- 不把链接层限制在 Rust。
- 不做跨 link 的 `links` manager / 全局 `connect` 服务 / 中心凭据库；组合只靠 row + session + 服务名。
- 不在本设计里实现模板、目录和 CI（#10）。

## 6. 开放问题

- **O1（阻塞非 Rust listen 顶替）**：需要 rutis 在 `rutis/sessions.open` 上暴露握手后的远端 id（例如 `near.id` / `ready()`）。在它出现前，非 Rust listen 不做同 id 顶替；`session_key(row)` 先到先得，第二条同 row 的 session 不会注册 handle。该 API 变更应作为 rutis 的独立 issue 讨论，不能靠解析 `Hello` 绕过。
- **O2（阻塞非 Rust 出站限流）**：`open(far)` 不能设置 `SessionOptions::outbound_limit`，session 的出站队列默认无上限；bridge 自己的队列无法限制它。Rust 用 `Session::options(...)`；非 Rust 需要 rutis 在 `open` 上补这个参数，或明确非 Rust 生产链接必须由传输层回压。
- **O3（已关闭）**：`link/tcp` 帧长度固定 4 字节大端；8 字节留作未来扩展，不作为当前可选项。
- **O3**：0.21 的异步回调在旧 runtime（0.20）上按同步处理；bridge 不能假设 `send` 一定异步。

## 7. 版本影响

| 版本 | 影响 |
| --- | --- |
| 0.19.3 | 服务 `sessions` → `rutis/sessions`；旧名不保留 |
| 0.20 | 卸载一次逆序栈；session 写线程；`outbound_limit` 默认无上限 |
| 0.21 | 收到的函数默认异步；错误文本改变，按 code |

## 关联

- issue [#1](https://github.com/arcships/rutis-network/issues/1)
- [SKILL.md](../../.agents/skills/rutis-link-plugin/SKILL.md)
- [link-plugin-api.md](../../.agents/skills/rutis-link-plugin/references/link-plugin-api.md)
- rutis `docs/guide/plugin-api.md#自建连接rutissessionsopen`
- rutis `docs/design-remote-session-2026-10-28.md` §4、§5
- rutis `crates/rutis-bridge/src/session/plugin/open.rs`
