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
- 0.20 起 session 有自己的写线程，`open(far)` 的出站积压默认无上限；bridge 必须自己限流。
- 0.21 起收到的函数默认异步：`send` 可以是 async，session 会 await，传输写阻塞由此回压。

### D7 listen 顶替

- Rust：`Session::handshake(&ctx, channel).await` → `Greeted::id()`；先卸同 id 旧 session，再用 `ctx.plugin(greeted)` 挂新的；旧 channel 以 `Close::Replaced`（4002）关闭。
- 非 Rust：0.21.0 的 `open(far)` 只返回 `{ receive, end }`，没有远端 id。因此**不能**按 `Hello` id 顶替；只能一条连接一个 session，并在文档里写明“同 id 的多条连接并存”。
- 禁止在 bridge 里解析 `Hello` 来拿 id；禁止用 adapter 的准入身份代替。

### D8 错误

- 0.21 改了几乎所有错误文本；按 `code` 和 `data` 匹配。
- `bad-value` 的 `data.reason`：`non-finite`、`cycle`、`too-deep`、`type`、`tag`、`limit`。
- `bad-result`：`data.service`、`data.method`、`data.expected`。
- 链接层错误要说清行/连接/adapter 地址，不带 token。

### D9 参考实现与一致性契约

- 先做 Rust 或 TS 一份参考 bridge；各语言模板归 #10。
- 一致性契约：边界、顺序、16 MiB 上限、异步 send、背压、close/半关闭、远端 id 命名、准入失败不建 session、卸载关闭连接、Rust listen 顶替。

### D10 localhost TCP / WebSocket 是第一波，HTTP 不是

- 核心已有 `websocket::dial` / `websocket::listen`，`rutis-host/listen` 也用它；不新增 `link/websocket`。
- 参考 bridge 先用 localhost TCP（#6）或现有 WebSocket 验证；两者都只绑 `127.0.0.1`，回环也要随机端口/一次性 token 准入。
- 纯 HTTP 请求/响应没有双向长连接和消息边界，不是 link；HTTP 只作为 WebSocket 升级或 gRPC 的承载。

## 3. 接口

Rust：

```rust
let channel = adapter.connect(&config).await?;   // 准入已完成
let session = ctx.plugin(Session::new(channel));  // session 生命周期 == channel 生命周期
```

TS：

```ts
const near = ctx.use('rutis/sessions').open({
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
- 不在本设计里实现模板、目录和 CI（#10）。

## 6. 开放问题

- **O1（阻塞非 Rust listen 顶替）**：需要 rutis 在 `rutis/sessions.open` 上暴露握手后的远端 id（例如 `near.id` / `ready()`）。在它出现前，非 Rust listen 不顶替。该 API 变更应作为 rutis 的独立 issue 讨论，不能靠解析 `Hello` 绕过。
- **O2**：`link/tcp` 的帧长度用 4 字节还是 8 字节？设计稿 #6 取 4 字节（16 MiB 上限足够），若以后要传大帧再扩。
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
