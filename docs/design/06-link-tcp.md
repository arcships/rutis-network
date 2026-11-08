# `link/tcp` 通用字节流 bridge：设计稿

状态：草案。日期：2026-11-08。基线：rutis 0.21.0。
关联 issue：[#6](https://github.com/arcships/rutis-network/issues/6)。依赖：[#1](https://github.com/arcships/rutis-network/issues/1)。

## 1. 目标

把任何“给一条 TCP 字节流”的 adapter（SSH 转发、Tailscale、Cloudflare access、ADB、usbmuxd、frp/rathole、ngrok）接到 rutis session 上，只写一次 framing、背压、close 和生命周期。

`link/tcp` **不做认证**；认证在它下面的 adapter。

## 2. 边界

```text
adapter（SSH/Tailscale/Cloudflare/ADB/frp）── TCP 字节流 ── link/tcp bridge ── rutis/sessions
```

- bridge 不知道对面是谁；不解析 session 协议；不做准入。
- 一条 TCP 连接 = 一个 bridge 实例 = 一个 session。
- 反向隧道只负责“把本地/公网 TCP 带过来”，不重复 bridge。

### 2.1 localhost TCP 是第一个 adapter

- 先做 `dial 127.0.0.1:<port>` / `listen 127.0.0.1:0`，作为一致性测试和本地开发的默认传输。
- 只绑 `127.0.0.1`。随机端口不是准入：本机任何进程都能扫到。要准入就用 Unix socket 权限，或在本 bridge 之前定义一个 token 帧；没有准入的 raw TCP loopback 只用于开发和测试。
- 它不引入新的 session 逻辑，只是 #6 bridge 的第一个 adapter 和测试底座。

### 2.2 WebSocket 已有，HTTP 不是 link

- 核心已有 `websocket::dial` / `websocket::listen`，`rutis-host/listen` 也用它；本 bridge 不重写 WebSocket。
- 纯 HTTP 请求/响应没有双向长连接和消息边界，不作为 link；HTTP 只承载 WebSocket 升级或 gRPC。

## 3. 帧格式

```text
+----------------+--------------------------+
| length: u32 BE | payload: length bytes    |
+----------------+--------------------------+
```

- `length` 是 payload 字节数，不含 4 字节头。
- 最大 16 MiB（`16 * 1024 * 1024`）；`length > 16 MiB` 在分配前拒绝并关闭。
- `length == 0` 允许：空消息有边界，session 可以传。
- 不做换行 framing；不做压缩；不做加密（TLS/SSH/Tailscale 在 adapter 层）。
- 为什么 4 字节：16 MiB 上限足够，4 字节比 8 字节省；以后要更大帧再升级版本/协商，不混用。

## 4. 发送

- 发送一条消息：写 4 字节大端长度，再写 payload；必须处理部分写（`write` 返回少于请求）。
- 发送队列有界（默认 64 条或 4 MiB，取先到者；这是 bridge 自己的策略，不是 `Channel` 的契约）；队列满时 `send` 等待。
- 0.21 起 `far.send` 可以是 async：session 会 await；写阻塞通过 Promise 回压，不要 fire-and-forget。
- 0.20 起 session 有写线程，出站队列默认无上限。Rust 用 `Session::options(SessionOptions { outbound_limit: Some(bytes) })`；非 Rust `open(far)` 没有这个参数，bridge 自己的队列不能限制 session 的队列（O2）。不要把“bridge 有界”当成“session 有界”。
- 写失败/连接关闭：让当前 `send` 失败，关闭连接，`near.end`。

## 5. 接收

- 循环读：先读满 4 字节头，再读满 `length` 字节；处理短读和粘包。
- 收到完整 payload 后 `near.receive(payload)`；`near.receive` 返回 Promise，满了会等（session 队列 64 条），这就是接收背压。
- `length` 超限：关闭，报错误；不要先分配 16 MiB 再检查。
- EOF：如果正在读一帧，说明截断，报错并关闭；如果帧边界 EOF，`near.end({kind:'done'})`。

## 6. close 与 EOF

session 没有半关闭语义，只有 `near.end({kind})`；不要设计半关闭协议。

- 远端 FIN（读端 EOF）→ `near.end({kind:'done'})`，随后关闭双向。
- 本端 session 卸载 → 关闭 socket 写端，尽量 drain，再关闭；只关一次。
- `near.end({kind:'replaced'})`：同 id 被顶替，关闭时映射为传输的 4002（如果传输有 close code）。
- `near.end({kind:'rejected', reason})`：准入后拒绝，映射 4003（如果有）。
- 一方向 EOF 就是整个连接结束；不要等另一方向再发 FIN，也不要重复关闭。

## 7. listen 侧

- accept 每条连接，建一个 bridge + 一个 session。
- Rust：`Session::handshake` 拿 `Greeted::id()`，同 id 新连接先卸旧 session、再挂新的；旧连接 `Close::Replaced`（4002）。
- 非 Rust：`open(far)` 不暴露 id，做不了同 id 顶替。`session_key(row)` 先到先得，同一个 row 下第二个 session 不会注册 handle，`<row>:<plugin>` 只会找到第一个；不要写“并存”。需要顶替的监听侧必须用 Rust（O1）。
- accept 失败（EMFILE 等）要退避，不要忙等。

## 8. 重连

- bridge 不重连；重连在 adapter（SSH/Tailscale/Cloudflare/frp）。
- 退避：0.5s → 30s 指数；认证被拒每 30s 再试；协议不兼容停止；连接满 60s 后重置退避。
- 新连接 = 新 bridge = 新 session；不要复用旧 session。

## 9. 反向隧道 wrapper

| 工具 | 给什么 | 准入 | 注意 |
| --- | --- | --- | --- |
| frp | 本地/公网 TCP | frpc token/TLS | 要 VPS；公网端口风险 |
| rathole | 本地/公网 TCP | token/TLS | 轻量；配置写清 |
| ngrok | 公网 TCP | authtoken | 临时 URL/限流；公开风险 |
| SSH `-R` | 远端转发到本地 | SSH host key/用户 key | 复用 link/ssh 的准入；不要重复认证 |
| Cloudflare access | 本地 TCP | Access token | 见 #5 |

wrapper 是 adapter/配置，不是新 crate；bridge 不变。

## 10. 参考实现结构

```text
link/tcp/
  bridge.ts        # framing + open(far) + lifecycle
  listen.ts        # accept + 每条连接一个 bridge（Rust 顶替）
  adapter.ts       # dial(host, port) / listen(host, port)
  errors.ts        # code + 结构化 data
  README.md        # 配置、错误、限制
```

Rust 版本用 `Channel` + `Session::new`；TS/Python 用 `rutis/sessions`。

## 11. 测试

- localhost TCP dial/listen 跑通，作为默认测试传输；只绑 127.0.0.1；无准入的 raw TCP 只用于测试，生产用 Unix socket 权限或 token 帧。
- 本地 TCP echo：单帧、空帧、连续帧、粘包/短读、16 MiB、16 MiB+1（16 MiB 是 bridge 策略）。
- 顺序：1000 条小消息顺序不变。
- 背压：慢接收方 + 快发送方，bridge 队列不超过上限；Rust 用 `SessionOptions::outbound_limit` 验证 session 也会结束，而不是无界增长。
- 异步 `send`：返回 Promise，session await；慢写不丢消息。
- close/EOF：远端 FIN → `near.end`；session 卸载 → socket 关闭；只关一次；没有半关闭协议。
- listen：多连接各自 session；Rust 同 id 顶替；非 Rust 明确不支持。
- 反向隧道：frp 或 rathole 任一跑通；SSH `-R` 或 ngrok 至少一个跑通；bridge 代码不改。
- 错误：截断帧、超长、连接重置，错误清楚且带 code。
- 不依赖公网；CI 用 loopback。

## 12. 非目标

- 不做认证/身份。
- 不做可靠 UDP/QUIC；那是 `link/quic`/P2P。
- 不实现 frp/rathole/ngrok 服务端。
- 不做压缩/加密/多路复用。
- 不新写 WebSocket（核心已有）；纯 HTTP 请求/响应不是 link。
- 不把 `link/tcp` 做成核心 trait。

## 13. 开放问题

- O1（已关闭）：发送队列上限 64 条 / 4 MiB，取先到者。
- O2（已关闭）：帧长度固定 4 字节；8 字节留作未来扩展。
- O3（已关闭）：session 没有半关闭；EOF 映射为 `near.end({kind:'done'})`，关闭双向。
- O4（阻塞）：非 Rust `open(far)` 不能设置 `outbound_limit`，session 出站队列默认无上限；生产用非 Rust link 前需要 rutis 补参数或传输层回压。
- O5（阻塞）：非 Rust 不能按 `Hello` id 顶替；`session_key(row)` 先到先得，需要顶替的监听侧必须用 Rust。

## 关联

- issue [#6](https://github.com/arcships/rutis-network/issues/6)
- [契约设计稿](01-link-plugin-contract.md)
- [SSH 设计稿](03-link-ssh.md)
- [Tailscale 设计稿](04-link-tailscale.md)
- [Cloudflare 设计稿](05-link-cloudflared.md)
- rutis `crates/rutis-bridge/src/channel/mod.rs`
