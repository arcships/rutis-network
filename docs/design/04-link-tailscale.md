# `link/tailscale`：设计稿

状态：草案。日期：2026-11-08。基线：rutis 0.21.0。
关联 issue：[#4](https://github.com/arcships/rutis-network/issues/4)。依赖：[#1](https://github.com/arcships/rutis-network/issues/1)、[#6](https://github.com/arcships/rutis-network/issues/6)。

## 1. 目标

给个人/小团队一个不用开公网端口、不用自己管证书和 DNS 的跨 NAT 方案。Tailscale 是 adapter，不是 rutis 的 underlay：link plugin 用 tailnet TCP 建连接，bridge 做 framing，交给 `rutis/sessions`。

## 2. 三种形态

### 2.1 system（首选）

- 使用系统 `tailscaled`；link plugin dial/listen tailnet TCP 地址（MagicDNS 名或 `100.x`）。
- 需要用户在机器上装 Tailscale 并登录；link plugin 只依赖本机 tailnet。
- 优点：不引入 Go 依赖；`tailscale status` 能看 direct/relay。
- 缺点：要求系统 daemon；容器/无 root 环境麻烦。

### 2.2 tsnet（Go，可选）

- 用 `tailscale.com/tsnet` 在进程内加入 tailnet；`Listen`/`Dial` 直接拿 TCP。
- 优点：不依赖系统 daemon；适合嵌入式/容器。
- 缺点：Go 依赖体积；控制面/状态目录要配置；不同语言要包一层。

### 2.3 sidecar（其他语言）

- 本机跑一个 `tsnet` sidecar，link plugin 连本地回环 TCP；sidecar 的地址/端口由配置给出。
- 优点：任何语言可用；sidecar 生命周期可以跟 link plugin 走。
- 缺点：多一个进程；本地回环的准入要限制（只绑 127.0.0.1，随机端口/令牌）。

## 3. 准入

| 项 | 做法 |
| --- | --- |
| 网络准入 | tailnet ACL / 设备身份；不引入 rutis token |
| 设备授权 | Tailscale 登录/节点授权；失败在 link 层报错 |
| 身份 | 仍由 session `Hello` 注册；不把 tailnet 节点名/用户映射成 session identity |
| sidecar | 只绑 127.0.0.1；用随机端口或一次性令牌，避免本机其它进程乱连 |

## 4. 连接与 framing

- 一条 tailnet TCP = 一个 session；用 #6 的长度前缀 framing（4 字节大端 + payload，最大 16 MiB）。
- 不要把现有 WebSocket 直接跑在 Tailscale 上再叫它 link/tailscale；adapter 只负责到达，bridge 负责消息边界。
- 0.21 异步 `send`：TCP 写阻塞通过 async `send` 回压；bridge 队列有界。

## 5. 生命周期与重连

- tailnet TCP 断开 → `near.end`；session 卸载 → 关闭连接。
- 重连在 link plugin：指数退避（0.5s → 30s）；ACL 拒绝/证书过期不要无限重试，报清楚。
- 0.20 卸载顺序：连接/daemon 句柄清理只注册一次；不在 cleanup 里注册/等待 cleanup。
- direct 与 DERP：`tailscale status` 区分；DERP 回退时吞吐/延迟会变差，日志里要能看出当前路径。

## 6. listen 侧

- system 模式可以用 Rust 的 `Session::handshake` 做同 id 顶替；非 Rust `open(far)` 不暴露 id，`session_key(row)` 先到先得，所以非 Rust 监听不支持同 id 顶替，必须用 Rust（O1）。不要写“并存”。
- tsnet/sidecar 的 Listener 在 adapter 层 accept，仍然要遵守“一个连接一个 session”。
- 不解析 session `Hello`；不拿 tailnet 节点名代替 id。

## 7. 错误

| 场景 | 处理 |
| --- | --- |
| Tailscale 未启动/未登录 | 报 `tailscale status` 的状态和建议 |
| MagicDNS 不解析 | 报名字和 DNS 错误；提示用 `100.x` |
| ACL 拒绝 | 报拒绝；不要当成网络超时重试 |
| DERP 回退 | 正常连接但记录路径；文档写清性能风险 |
| 证书/密钥过期 | 报过期；提示重新登录 |
| sidecar 未启动 | 报 sidecar 地址和启动方式 |

## 8. 安全边界

- tailnet ACL 是准入边界；rutis 信任 tailnet 放进来的连接。
- 不把 tailnet 身份传给 session；不在日志里写 auth key。
- sidecar 只绑回环，避免把本地端口暴露给整个 tailnet/局域网。
- Tailscale Funnel/Serve 是入口/部署，不是 link plugin；不要混进来。

## 9. 测试

- unit：fake TCP + 长度前缀 framing；边界/顺序/16 MiB/close/背压。
- system 模式：两台 tailnet 机器，一台 dial、一台 listen，跑通 `open`。
- tsnet 或 sidecar 至少一种跑通，证明不依赖系统 daemon。
- ACL 拒绝：连接失败，错误清楚。
- 断线重连：退避、认证失败不无限重试。
- CI 不依赖真实 tailnet；手工 e2e 步骤写进文档。

## 10. 非目标

- 不把 Tailscale 变成核心依赖。
- 不要求所有 rutis 用户装 Tailscale。
- 不做 Tailscale Serve/Funnel 封装。
- 不实现 Headscale 控制面；只写兼容性。
- 不把 tailnet 身份映射成 session identity。

## 11. 开放问题

- O1：sidecar 用固定协议还是直接暴露本地 TCP？固定协议可以限制只跑 tailnet 转发。
- O2：tsnet 的 Go 依赖是否值得为“无 daemon”场景引入，还是 sidecar 足够。
- O3：Headscale 的兼容性测试是否进 CI（本地容器）。

## 关联

- issue [#4](https://github.com/arcships/rutis-network/issues/4)
- [契约设计稿](01-link-plugin-contract.md)
- [TCP bridge 设计稿](06-link-tcp.md)
- [Cloudflare 设计稿](05-link-cloudflared.md)
- rutis `docs/guide/nodes.md`
