# `link/ssh`：设计稿

状态：草案。日期：2026-11-08。基线：rutis 0.21.0。
关联 issue：[#3](https://github.com/arcships/rutis-network/issues/3)。依赖：[#1](https://github.com/arcships/rutis-network/issues/1)、[#6](https://github.com/arcships/rutis-network/issues/6)。

## 1. 目标

让已经有 SSH 的用户不装 VPN、不开公网端口，用现有 SSH 把另一台机器接进 rutis。准入完全由 SSH 负责；rutis 不引入自己的 token。

## 2. 三种模式

### 2.1 stdio（首选参考实现）

```text
本端 link/ssh ── ssh host -- <rutis 运行时/桥> ── 远端
                 stdin/stdout 当字节流
```

- 本端起 `ssh host -- <命令>`，把 stdin/stdout 当双向字节流；framing 由远端命令决定：远端是 `pipe::framed` 兼容的 rutis 运行时/桥就用换行 framing，远端是 #6 bridge 就用 4 字节长度前缀。然后 `rutis/sessions.open(far)`。
- 远端命令必须是能说 session 协议的一端：一个 rutis 运行时、一个带 `rutis-bridge/session` 的桥，或另一个 rutis。
- 优点：不需要 `sshd` 开额外端口，不需要 `-L`/`-R`；SSH 的 host key、agent、`authorized_keys` 直接复用。
- session 握手本身是 10 秒（`Session::handshake`）；`Process::mount` / `rutis-host/spawn` 的 30 秒问候时限只适用于本端启动运行时进程，不适用于 SSH stdio。SSH 进程迟迟不问候要报错并结束进程，不能无限等。

### 2.2 forward

```text
本端 link/tcp ── 127.0.0.1:<local> ── ssh -L/-R ── 远端 TCP
```

- 用 `ssh -L` / `ssh -R` 把远端端口带到本地（或反向），再交给 #6 的 `link/tcp`。
- 适用于远端已经有 rutis 的 WebSocket/TCP 监听；link/ssh 只负责把端口带过来。
- 不在 link/ssh 里重复 framing/close/背压。

### 2.3 库模式（可选）

- Rust `russh`/`ssh2`、Go `x/crypto/ssh`、Python `asyncssh`、Node `ssh2`、Swift `swift-nio-ssh` 直接开 `direct-tcpip` channel。
- 好处：不用起子进程，能做连接复用；代价是每种语言一套 SSH 库，auth/known_hosts 行为要自己对齐。
- 先不做；stdio 和 forward 覆盖大多数场景。

## 3. 准入

| 项 | 做法 |
| --- | --- |
| host key | 用系统 `known_hosts`；不跳过校验；首次连接按 OpenSSH 的规则 |
| 用户认证 | SSH agent / key / `authorized_keys`；不引入 rutis token |
| 远端账户 | 由 SSH 决定；不映射成 session identity |
| 失败 | SSH 退出码/错误原样报出；不调用 `open`；不创建 session |

不把 SSH 用户名、host key 指纹、agent 身份传给 session 或 `<id>:<plugin>` 命名。

## 4. 帧格式

stdio 是字节流，必须 framing，规则由远端命令决定：

- 远端是 `pipe::framed` 兼容的 rutis 运行时/桥：用换行 framing，单条最大 16 MiB。session JSON 会转义字符串里的换行，token 之间不产生裸换行，所以换行分帧是安全的，和核心 `pipe::lines` 一致。
- 远端是 #6 bridge：用 4 字节大端长度 + payload，单条最大 16 MiB，超长在分配前拒绝。
- 两种不能混用；`ssh` 启动命令的配置里必须写清远端是哪一种。
- 空消息允许（长度 0）。
- 读到 EOF：`near.end({kind:'done'})`；写失败：关闭并让 session 卸载。

## 5. 生命周期与重连

- SSH 进程退出 / channel 关闭 → `near.end`；session 卸载 → 关闭 SSH 进程/channel。
- 重连在 link/ssh：默认指数退避（0.5s → 30s），认证失败不重试（与 rutis 的 dial 规则一致），协议不兼容停止。
- 0.20 卸载顺序：SSH 子进程的 kill/wait 只注册一次，不在 cleanup 里注册/等待另一个 cleanup。
- 0.21 异步 `send`：SSH stdin 写阻塞用 async `send` 回压；不要在 bridge 里无界排队。
- 子进程退出要 wait，避免僵尸；`ssh` 进程的输出（stderr）要能出现在 link plugin 的错误里，但不要混进 session 字节流。

## 6. listen 侧

- Rust 用 `Session::handshake` → `Greeted::id()` 才能同 id 顶替。
- 非 Rust 的 `open(far)` 不暴露远端 id：只能一条连接一个 session，文档写明“同 id 的连接并存”。
- 不解析 session `Hello`；不拿 SSH 用户/主机名代替 id。

## 7. 错误

| 场景 | 处理 |
| --- | --- |
| SSH 连接失败 / 认证失败 | 报 host、port、SSH 原始错误；不 `open` |
| 远端命令不存在 | 报远端命令和退出码 |
| 远端不问候 | 10 秒内报 session 握手超时（`the far end did not greet in time` 一类错误），并结束 SSH 进程 |
| 帧超长 | 关闭，报上限 |
| SSH 断开 | `near.end({kind:'done'})`；按退避重连 |
| 协议不兼容 | 报版本；不重试 |

0.21 错误文本不是契约；link plugin 自己的错误按 code/结构化字段，SSH 原始错误保留原文。

## 8. 安全边界

- SSH 是准入边界；rutis 完全信任 SSH 放进来的连接。
- 不把 SSH 私钥、agent socket、密码写进日志。
- 远端命令由用户配置；默认不接受远端任意命令注入（命令是配置的一部分，不是 session 数据）。
- Windows 用 OpenSSH 客户端时，stdio/进程信号行为要单独写清。

## 9. 测试

- 本地 sshd 或 fake channel：stdio 双向、边界/顺序、16 MiB 上限、close/EOF；两种 framing（换行、4 字节）各跑一次。
- 远端不问候：10 秒 session 握手超时并结束进程；`Process::mount` 的 30 秒只用于本端 spawn。
- 认证失败：不创建 session。
- `ssh -L` / `-R` + `link/tcp`：同一套 session 跑通。
- 断线重连：退避、认证失败不重试、协议不兼容停止。
- 平台矩阵：Linux + macOS 必跑；Windows OpenSSH 手工或 CI（如果可用），记录信号/进程行为差异。
- 不依赖公网 SSH 服务。

## 10. 非目标

- 不实现 SSH 服务端。
- 不做 SSH CA/证书颁发。
- 不把 SSH 认证映射成 rutis token。
- 不把 SSH 变成核心依赖。

## 11. 开放问题

- O1：stdio 模式下远端到底起什么？rutis 运行时需要什么参数才能从 stdin/stdout 说 session 协议。
- O2：是否复用 `rutis-host/spawn` 的 `pipe::spawn` 语义（socket/loopback），还是 SSH 自己开管道。
- O3：库模式是否值得为移动端/嵌入式做。

## 关联

- issue [#3](https://github.com/arcships/rutis-network/issues/3)
- [契约设计稿](01-link-plugin-contract.md)
- [TCP bridge 设计稿](06-link-tcp.md)
- rutis `docs/migration-0.20.md`（运行时问候时限）
- rutis `crates/rutis-bridge/src/pipe/mod.rs`
