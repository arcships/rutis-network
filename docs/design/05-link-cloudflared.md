# `link/cloudflared`：设计稿

状态：草案。日期：2026-11-08。基线：rutis 0.21.0。
关联 issue：[#5](https://github.com/arcships/rutis-network/issues/5)。依赖：[#1](https://github.com/arcships/rutis-network/issues/1)、[#6](https://github.com/arcships/rutis-network/issues/6)。

## 1. 目标

覆盖“拨号侧不能装 VPN、监听侧在 NAT 后不能开公网端口”的场景：监听侧用 Cloudflare Tunnel 出站，拨号侧用 WSS（WebSocket 升级）或 `cloudflared access tcp` 连入。

Cloudflare 只负责到达（和可选的 Access 准入）；link plugin 做 framing 和 `rutis/sessions.open(far)`；rutis 不需要知道 Cloudflare。

## 2. 三种形态

### 2.1 access（先做）

```text
拨号侧 link/tcp ── 127.0.0.1:<local> ── cloudflared access tcp ── Cloudflare ── 监听侧
```

- 拨号侧跑 `cloudflared access tcp --hostname <host> --url 127.0.0.1:<port>`。
- link plugin 连本地回环 TCP，走 #6 的 `link/tcp` bridge。
- 准入由 Cloudflare Access 服务令牌/身份负责；不把 Access 身份传给 session。
- 优点：拨号侧只要 `cloudflared`；不需要公网监听端口。
- 缺点：多一个进程；Access token 要管。

### 2.2 public（可选）

- 监听侧跑 `cloudflared tunnel`，把本地 rutis 的 WSS/TCP 暴露到 `wss://<host>`。
- link plugin 连 `wss://<host>`（WebSocket 升级）或 Cloudflare 的 TCP 承载；纯 HTTPS 请求/响应不是 link。
- 准入由 Cloudflare Access 或链接层自己的 token 负责；公开入口必须写清风险。
- 不把“公开入口 + 无 Access”当作默认推荐。

### 2.3 WARP

- WARP 是设备侧网络接入，不是 link plugin 的 adapter；文档写清它和 access/public 的区别。
- 不把 WARP 身份传给 session。

## 3. 准入

| 项 | 做法 |
| --- | --- |
| Access | 服务令牌/身份；token 只在 adapter 配置里 |
| public | Access 或链接层 token；默认要求 Access |
| 身份 | 仍由 session `Hello` 注册；不把 Access 邮箱/服务令牌映射成 `<id>` |
| 失败 | 关闭连接；不调用 `open`；不创建 session |

## 4. 连接与 framing

- access 模式拿到的是本地 TCP：用 #6 的 4 字节大端长度 + payload，最大 16 MiB。
- public 模式：WebSocket 消息已有边界，直接 `open`；如果用 TCP，再 framing。
- 0.21 异步 `send`：WebSocket/`cloudflared` 的写阻塞通过 async `send` 回压；bridge 队列有界。
- 长连接：Cloudflare 的 HTTP/2/WebSocket 代理有超时和缓冲，具体值随计划变化；文档写清，测试覆盖重连。

## 5. 生命周期与重连

- `cloudflared` 退出 / WebSocket 关闭 → `near.end`；session 卸载 → 关闭连接/进程。
- 重连在 link plugin：指数退避（0.5s → 30s）；Access 拒绝不要无限重试。
- 0.20 卸载顺序：`cloudflared` 子进程的 kill/wait 只注册一次；不在 cleanup 里注册/等待 cleanup。
- 子进程 stderr 要能进 link plugin 错误，不混进 session 字节流。

## 6. listen 侧

- 监听侧通常是 `cloudflared tunnel` 后面的 rutis listener；Rust 用 `Session::handshake` 才能同 id 顶替。
- 非 Rust `open(far)` 不暴露 id，`session_key(row)` 先到先得，所以非 Rust 监听不支持同 id 顶替，必须用 Rust（O1）。不要写“并存”。
- 不解析 session `Hello`；不拿 Access 身份代替 id。

## 7. 错误

| 场景 | 处理 |
| --- | --- |
| `cloudflared` 未安装/未启动 | 报二进制名和启动命令 |
| Access 拒绝 | 报 403/Access 错误；不重试 |
| Tunnel 断开 | `near.end`；退避重连 |
| WebSocket/HTTP2 代理超时 | 报超时；提示长连接限制 |
| 公开入口配置错误 | 报 hostname/route，不暴露 token |
| 帧超长 | 关闭，报 16 MiB 上限 |

## 8. 安全边界

- 公开入口是公网可达的；默认加 Access，不要裸露 rutis。
- Access token 只在 adapter 配置/环境变量里；不进 session、不进日志。
- `cloudflared` 子进程参数不要从 session 消息拼。
- 文档写清 Cloudflare 能看到什么（TLS 终止点、Access 日志）。

## 9. 测试

- 本地 fake tunnel / 本地 `cloudflared access` 能覆盖的部分：access + `link/tcp` 跑通 session。
- public 模式：本地 WSS 测试服务跑通；Access 拒绝错误清楚。
- 断线重连/退避；session 卸载关闭连接。
- 不依赖真实 Cloudflare 账号；手工 e2e 步骤写进文档。

## 10. 非目标

- 不实现 Cloudflare Tunnel 服务端。
- 不把 Cloudflare 变成核心依赖。
- 不把 Access 身份映射成 session identity。
- 不做 Workers/Pages 集成。
- 不把 HTTP 反代当成 WebSocket link 的唯一方案；access TCP 更通用。

## 11. 开放问题

- O1：Access 服务令牌用环境变量还是 `cloudflared` 自己的配置？如何避免进入日志。
- O2：public 模式默认强制 Access，还是允许链接层 token？两者如何组合。
- O3：长连接超时的具体值要不要写死，还是只写“按计划/实测”。

## 关联

- issue [#5](https://github.com/arcships/rutis-network/issues/5)
- [契约设计稿](01-link-plugin-contract.md)
- [TCP bridge 设计稿](06-link-tcp.md)
- [Tailscale 设计稿](04-link-tailscale.md)
- rutis `docs/guide/nodes.md`
