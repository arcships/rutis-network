# `link/grpc` 云控制面 / relay / 分布式云：设计稿

状态：草案。日期：2026-11-08。基线：rutis 0.21.0。
关联 issue：[#7](https://github.com/arcships/rutis-network/issues/7)。依赖：[#1](https://github.com/arcships/rutis-network/issues/1)。

## 1. 定位

gRPC 不是 NAT 穿透，也不是设备互联。它是“到一个可达云端点之后”的应用协议，适合：

- 宿主主动连云控制面（注册、路由、配额、观测）；
- 宿主 ↔ 宿主 via 云 relay；
- 宿主 → 云运行时；
- K8s/service mesh 内部；
- 分布式云的多区域 relay/故障切换。

优先级：**只在云业务成立时 P1；否则 P2**。不阻塞 #3–#6。

## 2. 协议形状

```proto
syntax = "proto3";
package rutis.link.v1;

service Link {
  // 一条 bidi stream = 一个 session（只存在于 A/B 两端）。
  rpc Tunnel(stream Frame) returns (stream Frame);
}

message Frame {
  bytes payload = 1;   // 一条 session 消息，边界由 gRPC 保持
}
```

- 一条 bidi stream 对应一个 session；session 只在 A/B 两端存在，relay 是透明转发，不挂 session。
- `payload` 就是 session 消息；gRPC 已有消息边界，不需要长度前缀。
- 配对信息放在 stream metadata，不放进 `payload`：`x-rutis-pair`（租户内配对键）、`x-rutis-target`（目标 endpoint）、`x-rutis-generation`（每次重连递增）。relay 只读这些 metadata。
- 协议版本放在 metadata 或第一帧；不要改 `payload` 的语义。

## 3. 准入

| 方式 | 做法 |
| --- | --- |
| mTLS | 客户端证书；服务端校验 CN/SAN；不把证书身份传给 session |
| OAuth/JWT | `authorization: Bearer`；校验 issuer/audience/scope |
| API key | metadata；只在 adapter 配置里 |
| 失败 | 返回 `UNAUTHENTICATED`/`PERMISSION_DENIED`；不创建 session |

身份仍由 session `Hello` 注册；gRPC 的 principal 不映射成 `<id>`。

## 4. 限制

- 消息上限不是 rutis 的事实，而是所选 gRPC 实现的配置（很多默认 4 MiB）；服务端/客户端都要显式配置到至少 16 MiB，或明确分片策略。
- HTTP/2 中间层/负载均衡有超时、缓冲、最大流时长；长连接要 keepalive，避免被 idle 超时切断。
- serverless（scale-to-zero）不适合长流；要么常驻，要么接受重连。
- gRPC-Web 不支持 bidi；浏览器场景用 WebSocket/WebTransport。
- 代理可能不支持 HTTP/2 trailer；错误用 status/trailer 时先测。

## 5. 生命周期与重连

- stream 断开 → `near.end`；session 卸载 → 关闭 stream。
- 重连在 link/grpc：指数退避（0.5s → 30s）；`UNAUTHENTICATED`/`PERMISSION_DENIED` 每 30s 再试；`UNAVAILABLE`/GOAWAY 按退避重试；连接满 60s 重置退避。
- 0.20 卸载顺序：stream/客户端清理只注册一次；不在 cleanup 里注册/等待 cleanup。
- 0.21 异步 `send`：gRPC 写阻塞通过 async `send` 回压；bridge 队列有界。
- 服务端 GOAWAY：让当前 session 卸载，按退避重连；不要静默丢消息。

## 6. 云端形态

### 6.1 控制面

- 宿主注册：id、region、capabilities、当前负载；
- 路由：按 id/region/租户选择 relay 或运行时；
- 配额/观测：连接数、消息数、字节数、错误码；
- 不下发 session 内容。

### 6.2 relay（透明配对）

- **模型**：relay 不挂 session、不握手、不解析 `payload`；A/B 各自与自己的 session 握手，`Hello` 端到端。relay 只把两条 stream 的 `payload` 原样对接。
- **配对**：`x-rutis-pair` + `x-rutis-target` 定位两条 stream；租户 id 来自准入，配对键只在租户内有效。
- **代次**：每次重连 `x-rutis-generation` 递增；同 pair 的新代次到达时，relay 关闭旧代次的两条 stream，再对接新代次。
- **失败**：配对超时/找不到对端时，两端都收到 `UNAVAILABLE` 和结构化 `data`（pair、target、generation、reason）；不创建 session。
- **不做**：relay 不做身份、不做 session 顶替、不解析 `Hello`；这些都在 A/B。
- **边界**：relay/控制面是独立的云服务或应用，不是本地链接层的 manager/注册表，也不替 link 管凭据；不违反 #1 D11。

### 6.3 云运行时

- gRPC stream 到云上的 rutis 运行时；和本地 `rutis-host/spawn` 的语义一致；
- 运行时问候：session 握手 10 秒；`Process::mount` / `rutis-host/spawn` 的 30 秒只适用于本端启动运行时进程。
- 插件运行在云上，session 仍按 `<id>:<plugin>` 命名。

### 6.4 多区域

- 最近区域、会话粘性、故障切换、数据驻留；
- 故障切换时旧 stream 关闭，新 stream 重新握手；session 不迁移状态；
- 文档写清哪些状态会丢（内存中的 session/插件状态）。

## 7. listen 侧

- relay 不挂 session；只有 A/B 端点用 `Session::handshake` / `open`。
- A/B 的 Rust 监听用 `Session::handshake` 拿 `Greeted::id()` 做同 id 顶替；非 Rust `open(far)` 不暴露 id，`session_key(row)` 先到先得，所以非 Rust 监听不支持同 id 顶替，必须用 Rust（O1）。
- 非 Rust 在 O1（握手结果 API）落地前也拿不到握手的结构化错误，无法可靠区分认证拒绝/协议不兼容；不要把 gRPC status 当握手结果，只能按传输错误退避并记为限制。
- relay 只按 `x-rutis-pair` / `x-rutis-generation` 配对；不解析 `Hello`，不拿 gRPC principal 代替 id。

## 8. 错误

| 场景 | 处理 |
| --- | --- |
| 未认证/权限不足 | `UNAUTHENTICATED`/`PERMISSION_DENIED`；不 `open`；每 30s 再试 |
| 消息超 4 MiB | 调大限制或分片；报清楚 |
| 代理超时/GOAWAY | `near.end`；退避重连 |
| relay 配对失败 | 两端明确错误；不挂起 |
| 云运行时问候超时 | 10 秒 session 握手超时；`Process::mount` 的 30 秒只用于本端 spawn |
| 协议版本不兼容 | 报版本；不重试 |

错误按 gRPC status + 结构化 detail；0.21 的 rutis 错误文本不是契约。

## 9. 测试

- 本地 gRPC 测试服务：dial、bidi stream、`open(far)`、远端插件 `<id>:<plugin>`。
- mTLS 或 OAuth 失败：拒绝；session 不感知。
- 16 MiB：调大限制后跑通，或分片策略验证。
- stream 断开：session 卸载；重连；relay 重新配对。
- GOAWAY/超时：退避；不丢消息（在途消息要么确认，要么报错）。
- 不依赖真实云账号；CI 用本地服务。

## 10. 非目标

- 不做 NAT 穿透/设备互联。
- 不把 gRPC 身份映射成 session identity。
- 不先建完整多租户云产品；先协议适配 + 测试服务。
- 不把 gRPC-Web 当浏览器 bidi 方案。
- 不在一个 stream 里多路复用多个 session（先不做）。

## 11. 开放问题

- O1：proto 归 rutis-network 还是 rutis 主仓库？版本策略？
- O2（已关闭）：relay 不解析 `payload`；只按 metadata/租户路由。
- O3：一条 stream 一个 session 的成本（每 session 一个 HTTP/2 stream）是否可接受。
- O4：多区域故障切换时，是否允许 session 状态迁移？先不做。

## 关联

- issue [#7](https://github.com/arcships/rutis-network/issues/7)
- [契约设计稿](01-link-plugin-contract.md)
- [TCP bridge 设计稿](06-link-tcp.md)
- rutis `docs/guide/nodes.md`
