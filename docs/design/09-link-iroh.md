# `link/iroh` Rust P2P / relay 原型：设计稿

状态：草案。日期：2026-11-08。基线：rutis 0.21.0。
关联 issue：[#9](https://github.com/arcships/rutis-network/issues/9)。依赖：[#1](https://github.com/arcships/rutis-network/issues/1)。

## 1. 定位

给“不能装系统 VPN、又不想走 Cloudflare/ngrok 中心入口”的用户一个可嵌入的 P2P/relay 传输。iroh 是 Rust 的 QUIC + NAT 穿透 + relay 库，按公钥拨号，端到端加密，有 FFI。

**先原型，不承诺生产**：NAT 穿透在推进，直连不稳会回退 relay，DNS/证书能力随版本变化。

## 2. 连接形状

```text
Endpoint A ── QUIC (ALPN rutis/link/1) ── Endpoint B
               一条双向流 = 一个 session
```

- `Endpoint` + `ALPN`；拨号方知道对方的 `NodeId`/ticket。
- 一条 QUIC 双向流对应一个 session；不要在一个流里多路复用多个 session。
- 准入：endpoint 公钥 + 应用层 token（可选）；不把 iroh `NodeId` 传给 session。
- 远端在 `Hello` 注册 id；`NodeId` 不是 session id。

## 3. 帧格式

- QUIC 流是字节流：用 #6 的 4 字节大端长度 + payload，最大 16 MiB。
- 一条流 = 一个 bridge 实例 = 一个 session；流关闭 → `near.end`。
- 0.21 异步 `send`：QUIC 写阻塞通过 async `send` 回压；bridge 队列有界。
- 不在 iroh 层做压缩/加密之外的封装（QUIC 已有 TLS）。

## 4. 直达与 relay

| 形态 | 说明 | 风险 |
| --- | --- | --- |
| 直连 P2P | 打洞成功，端到端加密 | 打洞失败率随网络变化 |
| relay 回退 | 不能打洞时经 relay 转发 | 延迟/吞吐下降；relay 可见流量元数据（仍加密） |
| 公共 relay | 默认/测试 | 依赖第三方可用性/配额 |
| 自建 relay | 生产可控 | 运维成本 |

日志要能看出当前是直连还是 relay；文档写清 relay 的隐私/成本。

## 5. 生命周期与重连

- 流/连接关闭 → `near.end`；session 卸载 → 关闭流/连接。
- 重连在 link/iroh：指数退避（0.5s → 30s）；endpoint 认证失败不重试；relay 不可用按退避。
- 0.20 卸载顺序：endpoint/连接清理只注册一次；不在 cleanup 里注册/等待 cleanup。
- 0.21 异步 `send`：写阻塞回压。

## 6. listen 侧

- Rust 用 `Session::handshake` 才能同 id 顶替；非 Rust `open(far)` 不暴露 id，只能一条连接一个 session，文档写明。
- 不解析 `Hello`；不拿 `NodeId` 代替 id。

## 7. 成熟度与依赖

- iroh 的 NAT 穿透/relay/DNS 能力随版本变化；原型要固定版本并记录。
- 依赖体积：QUIC/TLS/crypto；移动端 FFI 要评估二进制大小。
- 不做生产 SLA；先 benchmark（直连/relay 的延迟、吞吐、成功率）。

## 8. 错误

| 场景 | 处理 |
| --- | --- |
| 对方不在线/NodeId 不解析 | 报 id 和 DNS/发现错误 |
| 打洞失败 | 记录并回退 relay（如果配置允许） |
| relay 不可用 | 报 relay 地址；退避 |
| 认证失败 | 报 endpoint 公钥/ token；不重试 |
| 流超时/重置 | `near.end`；重连 |
| 帧超长 | 关闭，报 16 MiB |

## 9. 测试

- 两个本地 endpoint：一条 QUIC 流、边界/顺序/16 MiB/close/背压。
- 直连与 relay 各跑一次；日志能区分。
- 断线重连/退避；session 卸载关闭流。
- 不依赖 n0 生产账号/公共 relay；用本地/自建测试 relay。
- benchmark：延迟/吞吐/成功率（直连 vs relay）。
- 至少一个手工 e2e 步骤。

## 10. 非目标

- 不把 iroh 变成核心依赖。
- 不做 rutis token/身份映射。
- 不承诺生产可用。
- 不实现 relay 服务端（先用公共/自建测试 relay）。
- 不做 libp2p/WebRTC 实现；只写对比。

## 11. 开放问题

- O1：用 iroh 的 ticket 还是 `NodeId` 作为拨号地址？是否要额外的应用层发现。
- O2：公共 relay 还是自建 relay 作为默认测试路径。
- O3：移动端 FFI 的二进制体积/功耗是否可接受。
- O4：是否需要 QUIC 多流（一个 endpoint 多条 session）还是每 session 一条连接。

## 关联

- issue [#9](https://github.com/arcships/rutis-network/issues/9)
- [契约设计稿](01-link-plugin-contract.md)
- [TCP bridge 设计稿](06-link-tcp.md)
- [gRPC 设计稿](07-link-grpc.md)
