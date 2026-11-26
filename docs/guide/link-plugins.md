# 开发连接插件

[English](link-plugins.en.md)

本指南的 TCP 和 Rust 参考示例固定在 rutis 0.21.0（会话协议 4、插件 API 3）。两端使用同一会话协议。0.23.0 使用协议 5，与协议 4 的端点握手会失败。[版本迁移](https://github.com/arcships/rutis/blob/v0.23.0/docs/migration-0.23.md)

| 组件 | 职责 |
| --- | --- |
| adapter | 通过 CLI、SDK 或 sidecar 到达对端，验证准入，管理心跳与重连，每次成功交出一条新连接。 |
| bridge | 保持二进制消息边界与顺序，施加背压，把一条已准入的连接交给一个会话，处理断开与卸载。 |
| rutis 会话 | 握手、挂载远端插件、传播服务与事件。对端在 Hello 中注册自己的 ID，导入的插件名为 `<远端 ID>:<插件>`。 |
| 应用插件 | 使用服务。配置行 `<连接行>:<插件>` 指定要在对端运行的插件。 |

准入成功等于信任对端注册的身份。adapter 必须在交接前完成验证；失败即关闭连接。证书和 token 只在 adapter 内使用。随机端口只参与寻址。Rust 的 Channel 收发有边界的不透明字节，Greeted 在握手完成后提供身份。[Channel](https://github.com/arcships/rutis/blob/v0.21.0/crates/rutis-bridge/src/channel/mod.rs) · [Session 与 Greeted](https://github.com/arcships/rutis/blob/v0.21.0/crates/rutis-bridge/src/session/plugin/mod.rs)

## Go 版 libp2p

`go:link-libp2p` 使用 QUIC 直连或指定的 Circuit Relay v2，将一个通过 Peer ID 校验的对端挂为会话。需要 Go 1.26 和 rutis-host 0.23.0。[构建、配置和管理接口](../design/09-p2p-plugins.md)

## 运行参考实现

在仓库根目录运行以下命令。Node 示例使用 Node ≥22.18、npm、OpenSSL 和支持 rutis-host 控制 socket 的 Unix 系统。Rust 示例使用 Cargo。

```sh
npm ci --prefix link/tcp
npm --prefix link/tcp run typecheck
npm --prefix link/tcp test
cargo test --locked --manifest-path examples/rust-listen/Cargo.toml
```

Node 测试生成临时凭据，在 `127.0.0.1` 启动两个真实宿主。拨号方校验服务端证书和主机名，监听方校验客户端证书。只有通过本端验证的 socket 才进入 bridge。测试通过另一端的 `witness.echo` 服务取回该次运行的唯一值。Rust 测试通过已有 WebSocket 传输接收三个已准入连接，验证相同 ID 的替换和不同 ID 的隔离。[TS 拨号插件](../../link/tcp/dial.ts) · [两宿主测试](../../link/tcp/test/hosts.test.mjs) · [Rust 监听示例](../../examples/rust-listen/tests/loopback.rs)

使用单次拨号示例时，在 `rutis.json` 启用 Node 运行时并添加以下行。将端口和 CA 路径指向本地 TLS 监听器，服务端证书应覆盖 `localhost`。监听方需验证客户端证书，并使用相同的分帧格式。

```json
{
  "id": "wire",
  "name": "/absolute/path/to/rutis-network/link/tcp/dial.ts",
  "config": {
    "port": 7443,
    "ca": "/path/to/peer-ca.pem",
    "cert": "/path/to/client.pem",
    "key": "/path/to/client.key"
  }
}
```

## TypeScript 与 Python 的交接

在插件的 `inject` 声明 `rutis/sessions`。服务的 `open(far)` 挂载一个子会话并返回 `near`。该返回值表示可以收发消息；此时握手仍可能失败。Python 使用同样的回调形状。[open 实现](https://github.com/arcships/rutis/blob/v0.21.0/crates/rutis-bridge/src/session/plugin/open.rs)

| 方向 | 调用与完成条件 |
| --- | --- |
| 会话 → 网络 | `far.send(bytes)` 完成后才发送下一条；`far.close(why)` 释放连接。 |
| 网络 → 会话 | 每条完整消息依次等待 `near.receive(bytes)` 入队；网络结束时调用一次 `near.end(why)`。 |

在 `apply` 中，取得已验证的 socket 后可写成：

```ts
ctx.effect(() => { socket.destroy() })
const link = await openTcp(socket, ctx.use('rutis/sessions'))
link.done.catch(reportTransportError)
```

Node SDK 在卸载时直接调用传给 `effect` 的清理函数。清理只注册一次，依次逆序执行。0.21 的远程回调默认异步，需要等待写完成和接收入队。参考实现一次只写一帧，最大 16 MiB，大于 4 MiB 的合法帧同样能取得进展。这个界限仅覆盖 bridge 的在途写入。0.21 的 `open(far)` 使用默认无界的会话出站队列；要求端到端内存上限的部署应选择能设置 `outbound_limit` 的 Rust 接口。[TCP bridge](../../link/tcp/bridge.ts)

## Rust 监听和替换

```rust
let view = ctx.plugin(Session::new(admitted_channel));
(&view).await?;
```

需要按 ID 替换时，先调用 `Session::handshake(&ctx, admitted_channel).await?`，读取 `greeted.id()`。完整示例按 ID 保存挂载句柄：新连接握手成功后，给对应旧连接标记替换，等待旧会话及子插件卸载；旧通道以 `Close::Replaced` 关闭，随后挂载新会话。握手失败保留原会话。示例串行处理已准入连接；需要并发的监听器可并发握手，并按 ID 串行替换。需要显式出站上限时用 `Session::new(channel).options(options)` 或 `Session::handshake_with`。[监听实现](../../examples/rust-listen/src/lib.rs) · [已有 WebSocket 准入与拨号示例](../../examples/rust-listen/tests/loopback.rs)

0.21 的非 Rust 接口只返回 receive 和 end；同一连接行只有第一个会话注册寻址句柄。需要同 ID 替换的监听端应使用 Rust。SSH 用户名、证书主体和会话载荷都不应被 bridge 用于推断该 ID。

## 传输与故障的一致性要求

| 条件 | 可观察行为 |
| --- | --- |
| 短读、粘包、空帧 | TCP bridge 读取 4 字节大端长度和指定字节数，按发送顺序交付完整消息。基于换行的 pipe 使用对端约定的原有格式。 |
| 超限、截断 | 长度超过 16 MiB 时，在分配载荷前以 `frame-too-large` 失败。帧内 EOF 以 `truncated-frame` 失败。两者均关闭连接，释放会话。 |
| 慢端 | 写 Promise 随 socket 写入完成而完成；读端等待 receive，然后再交付下一条。新连接重新分帧，旧消息不重放。 |
| 结束与卸载 | 帧边界 EOF 对应 `end({kind:'done'})`。任一方向结束都关闭整个通道。会话或父插件卸载会关闭 socket，重复清理无副作用。 |
| 准入、命名与替换 | 准入失败时创建的会话数为零。会话以 Hello ID 命名远端插件。已准入的新连接握手失败时原会话继续工作；同 ID 替换仅影响对应旧会话。 |
| 断线重试 | 由 adapter 决定重连。约定为 0.5 秒起指数退避至 30 秒，稳定连接 60 秒后重置。已知准入拒绝每 30 秒重试，已知协议不兼容停止。非 Rust 无结构化会话握手结果，未知断开按传输错误处理。 |

`done`、`replaced`、`rejected`（带 reason）是 rutis 的关闭原因。TCP 字节流只能传达断开，WebSocket 可以传输 4002 或 4003。诊断记录连接地址、连接行和错误 code，凭据留在适配器。按 rutis 错误的 `code` 和 `data` 匹配，例如 `not-declared` 带 `data.service`。单次拨号示例把传输错误输出为 `link/tcp 127.0.0.1:<port> <code>`。跨语言测试套件在 [issue #10](https://github.com/arcships/rutis-network/issues/10) 跟踪。
