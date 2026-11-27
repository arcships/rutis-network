# 开发连接插件

[English](link-plugins.en.md)

插件和示例固定使用 rutis 0.23.0（会话协议 5、插件 API 3）。两端宿主和各语言运行时需要一起升级，使用协议 4 的端点会在握手时被拒绝。[版本迁移](https://github.com/arcships/rutis/blob/v0.23.0/docs/migration-0.23.md)

| 组件 | 职责 |
| --- | --- |
| adapter | 通过 CLI、SDK 或 sidecar 到达对端，验证准入，管理心跳与重连，每次成功交出一条新连接。 |
| bridge | 保持二进制消息边界与顺序，施加背压，把一条已准入的连接交给一个会话，处理断开与卸载。 |
| rutis 会话 | 握手、挂载远端插件、传播服务与事件。对端在 Hello 中注册自己的 ID，导入的插件名为 `<远端 ID>:<插件>`。 |
| 应用插件 | 使用服务。配置行 `<连接行>:<插件>` 指定要在对端运行的插件。 |

准入成功等于信任对端注册的身份。adapter 必须在交接前完成验证；失败即关闭连接。证书和 token 只在 adapter 内使用。随机端口只参与寻址。Rust 的 Channel 收发有边界的不透明字节，Greeted 在握手完成后提供身份。[Channel](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/channel/mod.rs) · [Session 与 Greeted](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/session/plugin/mod.rs)

## Go 版 Tailscale

`go:link-tailscale` 通过本机 tailnet 按标签发现 worker，为每台建立独立连接，也支持指定单个节点。两种配置都可使用系统 tailscaled 或独立 tsnet 节点。管理插件通过 `rutis/loader` 创建、重载和移除连接行。[配置双端与管理连接](../design/04-link-tailscale.md)

## Go SSH 插件

`go:link-ssh-dial` 通过 SSH 连接目标 listener。使用 VPS 中继时，接收端的 `go:link-ssh-listen` 申请远端回环端口并直接接入 rutis 会话，`go:link-ssh-tunnel` 则将入站流转发给现有 TCP 服务。两端都拨入 VPS 原有的 SSH 端口。每个行实例独立提供配置、启停、状态和重连控制。[两端配置、控制接口与测试](../design/03-link-ssh.md)

## WebRTC 浏览器与原生连接

网页可与另一网页或原生宿主双向调用 rutis 服务。`go:link-webrtc` 和页面适配器都支持自动连接、动态改配、直连和强制 TURN 中继。配对信令可复用应用现有登录，示例提供 HTTP 信令，支持在同一会话内续签临时 TURN 凭据。[构建、配置、运行示例和管理接口](../design/08-link-webrtc.md)

## Go 版 libp2p

`go:link-libp2p` 使用 QUIC 直连或指定的 Circuit Relay v2，将一个通过 Peer ID 校验的对端挂为会话。需要 Go 1.26 和 rutis-host 0.23.0。[构建、配置和管理接口](../design/09-p2p-plugins.md)

## 安装 Go 插件

在仓库根目录使用 Go 1.24 或更高版本编译 TCP 插件。将生成的二进制放到两端宿主的 `rutis.json` 旁的 `plugins/go` 目录。只有构建机器需要 Go 工具链。

```sh
mkdir -p plugins/go
go -C link/tcp build -o ../../plugins/go/link-tcp ./cmd/link-tcp
```

在拨号宿主配置监听端口、用于验证服务端的 CA 和本机的客户端证书与私钥。默认只拨号一次。

```json
{
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [{
    "id": "wire",
    "name": "go:link-tcp-dial",
    "config": {
      "port": 7443,
      "ca": "/path/to/peer-ca.pem",
      "cert": "/path/to/client.pem",
      "key": "/path/to/client.key"
    }
  }]
}
```

## 从任意 rutis 插件语言控制连接

在 TCP 行配置 `controlService`，例如 `"office-tcp"`。需要控制连接时，把该服务名加入已有调用方的 `inject`，并在两行中为它设置相同的 `isolate` 标签。调用方用自身语言的 SDK 配置目标、启停传输、查询或等待状态变化。只需自动连接时，也可直接给 TCP 行配置端口，由宿主独立启动。

托管拨号默认自动连接并重试。运行中改址会结束旧连接，再尝试新目标，显式停止后改址则等待下次启动。重载后使用部署配置。同一插件可通过 `rutis/loader` 挂载实例，由单独的 `go:link-tcp-attach` 行在管理插件启动或重启时交付已有连接的句柄。若要将 `wire:<plugin>` 行迁到另一端，先等待 TCP 行卸载，再以同一行 ID 创建新连接。[宿主配置、动态挂载与控制接口](../design/06-link-tcp-integration.md)

## TCP 字节流 bridge

监听宿主采用相同的运行时配置，行名使用 `go:link-tcp-listen`。配置服务端证书、私钥和专用于获准客户端的 CA。监听端绑定 IPv4 回环，拨号端可用 `host` 指向 DNS、IPv4 或 IPv6 地址。监听端口填 0 时由系统选择，启动后输出 `link/tcp listening 127.0.0.1:<port>`。每个通过准入的客户端独立挂载会话。卸载行会关闭监听器、进行中的 TLS 握手和已接入的连接。[Go 适配器](../../link/tcp/adapter.go) · [Go bridge](../../link/tcp/bridge.go)

在仓库根目录运行以下检查。集成测试使用 Node ≥22.18、npm、OpenSSL 和支持控制 socket 的 Unix 宿主。测试编译 Go 二进制，启动真实 rutis 宿主，通过 Go 拨号插件和单次拨号的 TypeScript 参考实现调用远端服务。Rust 示例使用 Cargo。[TypeScript 参考实现](../../link/tcp/dial.ts) · [宿主测试](../../link/tcp/test/hosts.test.mjs)

```sh
go -C link/tcp test -race ./...
npm ci --prefix link/tcp
npm --prefix link/tcp run typecheck
npm --prefix link/tcp test
cargo test --locked --manifest-path examples/rust-listen/Cargo.toml
```

在拨号配置中设置 `"reconnect": true` 即可常驻重拨。只给端口时校验 `localhost`，显式指定 `host` 时默认按该名称或 IP 校验证书。通过回环转发其他服务时，用 `servername` 指定对端证书上的预期 DNS 名称。每次尝试会重新读取证书和私钥，可在轮换后恢复连接。每次连接创建新的会话和帧流。临时错误的输出形如 `link/tcp 127.0.0.1:7443 ECONNREFUSED (retry in 500ms)`。证书拒绝等待 30 秒，移除行会取消重试并关闭 socket。完整的帧格式和状态约定见 [TCP 设计](../design/06-link-tcp.md)。

TCP 允许空帧，每帧最多 16 MiB。在 0.23.0 上传大数据时，应用可以在同一会话上逐块发送，接收方按顺序重组。每块须为编码和元数据留出空间。集成示例用最多 4 MiB 的块完整传输了 17 MiB。单次大调用的自动分块等待 `sessions.open` 支持声明帧上限。[分块回显示例](../../link/tcp/test/fixtures/large.mjs) · [上游进展](https://github.com/arcships/rutis/issues/1411)

## 反向隧道

服务端机器运行 rutis 监听行和隧道客户端。中继机器运行隧道服务端，提供本地转发端口给 rutis 拨号行。拨号行连接转发端口，仍验证服务端机器的 TLS 证书。隧道凭据控制谁能建立转发；其他本地进程若能接触转发端口，应用监听器仍需验证该进程出示的客户端证书。

| 路由 | 使用方法 |
| --- | --- |
| frp | 中继运行 `frps -c link/tcp/tunnels/frps.toml`，服务端机器运行 `frpc -c link/tcp/tunnels/frpc.toml`。两份配置可在同一台机器直接配合使用。双方设置 `FRP_CONTROL_PORT`、`LINK_FORWARD_PORT` 和 `FRP_TOKEN_FILE`（只允许所属用户读取的文件，双方使用相同随机 token）。frps 还需 `FRP_CERT_FILE` 和 `FRP_KEY_FILE`，frpc 还需 `FRP_CA_FILE` 和 `LINK_LISTEN_PORT`。示例将控制和转发端口都绑定回环。跨机器时，把控制面的绑定地址、客户端连接地址和校验的服务端名称改为预期的私网路由。若拨号行位于中继机器，`proxyBindAddr` 保持回环。[frps](../../link/tcp/tunnels/frps.toml) · [frpc](../../link/tcp/tunnels/frpc.toml) |
| SSH 反向转发 | 在服务端机器执行 `link/tcp/tunnels/ssh-reverse.sh ~/.ssh/config relay 17443 7443`。先配置中继别名、授权用户密钥和受信任的 SSH 宿主密钥，SSH 服务端保留 `GatewayPorts no`。中继上的拨号行连接 17443，经隧道到达服务端本机的 7443。转发端口填 0 可让中继分配端口。[OpenSSH wrapper](../../link/tcp/tunnels/ssh-reverse.sh) |
| rathole / ngrok | rathole 将服务绑定地址转发到客户端监听端口，使用服务 token 和受保护的传输。ngrok TCP 通过账号凭据和供应商访问策略建立入口。绑定公网会让外部客户端接触转发端口，应保留应用双向 TLS 并限制入口范围。 |

运行真实隧道验收时，PATH 需要包含 `frpc`、`frps`（实测 0.72.0）、`ssh` 和 `ssh-keygen`。默认使用 `/usr/sbin/sshd`，也可用 `SSHD` 指定测试守护进程的路径。当前用户须能启动测试守护进程。两项测试都在回环上生成临时密钥、启动中继与两个宿主，并校验远端服务调用返回值。无需外部账号。在仓库根目录执行：

```sh
npm --prefix link/tcp run test:tunnels
```

## TypeScript 与 Python 的交接

在插件的 `inject` 声明 `rutis/sessions`。服务的 `open(far)` 挂载一个子会话并返回 `near`。该返回值表示可以收发消息；此时握手仍可能失败。Python 使用同样的回调形状。[open 实现](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/session/plugin/open.rs)

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

Node SDK 在卸载时直接调用传给 `effect` 的清理函数。清理只注册一次，依次逆序执行。0.21 的远程回调默认异步，需要等待写完成和接收入队。参考实现一次只写一帧，最大 16 MiB，大于 4 MiB 的合法帧同样能取得进展。这个界限仅覆盖 bridge 的在途写入。0.23 的 `open(far)` 使用默认无界的会话出站队列；要求端到端内存上限的部署应选择能设置 `outbound_limit` 的 Rust 接口。[TCP bridge](../../link/tcp/bridge.ts)

## Rust 监听和替换

```rust
let view = ctx.plugin(Session::new(admitted_channel));
(&view).await?;
```

需要按 ID 替换时，先调用 `Session::handshake(&ctx, admitted_channel).await?`，读取 `greeted.id()`。完整示例按 ID 保存挂载句柄：新连接握手成功后，给对应旧连接标记替换，等待旧会话及子插件卸载；旧通道以 `Close::Replaced` 关闭，随后挂载新会话。握手失败保留原会话。示例串行处理已准入连接；需要并发的监听器可并发握手，并按 ID 串行替换。需要显式出站上限时用 `Session::new(channel).options(options)` 或 `Session::handshake_with`。[监听实现](../../examples/rust-listen/src/lib.rs) · [已有 WebSocket 准入与拨号示例](../../examples/rust-listen/tests/loopback.rs)

0.23 的非 Rust 接口只返回 receive 和 end；同一连接行只有第一个会话注册寻址句柄。需要同 ID 替换的监听端应使用 Rust。SSH 用户名、证书主体和会话载荷都不应被 bridge 用于推断该 ID。

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

## rutis 0.23 的 Cloudflare Tunnel

用 Go 1.24 或更新版本构建 `go:link-cloudflared`，在两端安装官方 `cloudflared`。源站 connector 行从文件读取命名隧道 token，拨号行用服务令牌文件建立本地 Access TCP 转发。可以使用宿主内置 WebSocket 行互连，或接入 #16 的 TCP 行使用端到端 mTLS。在管理服务及本地调用方所在行设置相同的隔离作用域。[两端配置与 Go 服务调用示例](../design/05-cloudflare-plugin-control.md)

在仓库根目录运行 `go -C link/cloudflared test -race ./...`。Linux 或 macOS 上可用 `RUTIS_HOST` 指定 rutis-host 0.23.0 的绝对路径，并将 `cloudflared` 放入 PATH，以运行无需云账号的集成测试。再用 `RUTIS_TCP_PLUGIN` 指定 #16 二进制的绝对路径，可同时测试 mTLS 配置。执行 `go -C link/cloudflared test -race -count=1 ./...`。

## 设备插件连接 Go 云服务内的插件

设备插件调用云端插件的 `jobs.submitJob`。云端插件把任务交给同一进程里的调度器，再通过同一条 gRPC 流回调设备插件，报告排队进度。云服务使用同一端口提供普通 gRPC 健康检查。单独卸载云端插件时，rutis 服务撤下，调度器和健康检查继续工作。[云应用和内嵌插件](../../link/grpc/example/cloud/main.go) · [设备插件](../../link/grpc/example/device/main.go)

接入已有 Go 服务时，在启动应用自己的 gRPC server 前调用 `grpclink.Register(server, clientDNSName)`。配置双向 TLS，把 gRPC 收发消息上限都设为 `grpclink.MaxWireSize`。云端插件在 `Inject` 声明 `rutis/sessions`，在 `Apply` 中调用 `endpoint.Bind(row)`。监听端口和证书由云服务管理。每个已绑定的 endpoint 同时接纳一台设备，设备证书须经受信 CA 签发并匹配配置的名称。设备可以挂载 `go:link-grpc` 行，也可以在插件里调用 `MountClient`，通过返回对象在本地执行 `Start`、`Stop` 和 `Status`。

云服务停机时，先调用 `endpoint.Drain(ctx)`，再调用 `server.GracefulStop()`。Drain 永久停止该端点接纳设备，关闭当前隧道，并等待 rutis 会话清理完成。等待超时后可再次调用。清理期间，同一服务器上的其他 RPC 仍可用。日常单独卸载插件时，rutis 会自动解绑，之后可以重新挂载。

在仓库根目录使用 Go 1.26 或更新版本构建。两端各有一份 rutis 0.23.0 项目配置。把[云端配置](../../link/grpc/example/cloud/rutis.json)和[设备配置](../../link/grpc/example/device/rutis.json)里的 PEM 路径改为受信 CA 和各自的证书、私钥。示例在回环地址监听，证书 DNS 名分别为 `cloud.test` 和 `device.test`。分开部署时，填写云端可达的监听地址和设备的拨号目标。

```sh
go -C link/grpc build -o example/cloud/plugins/go/cloud-service ./example/cloud
go -C link/grpc build -o example/device/plugins/go/device ./example/device
go -C link/grpc build -o example/device/plugins/go/link-grpc ./cmd/link-grpc
```

在两个终端分别执行 `rutis-host run link/grpc/example/cloud/rutis.json` 和 `rutis-host run link/grpc/example/device/rutis.json`。在设备上执行 `rutis-host call --config link/grpc/example/device/rutis.json device.submit '"build"'`。刚启动的服务返回 `{"id":"job-1","progress":"job-1 queued"}`。在 macOS 或 Linux 上把 `RUTIS_HOST` 设为 rutis-host 的绝对路径，执行 `go -C link/grpc test -race -count=1 ./...`，即可使用临时证书验证两端和重连。

一条获准的流承载一个会话，每个 protobuf 帧携带一条 rutis 消息，上限 16 MiB。设备断线后按 0.5–30 秒退避重连。若云端返回 `PermissionDenied` 或 `Unauthenticated`，即使隧道已建立，设备也会记录该状态码，等待 30 秒再重试。设备每次重连都会重新读取 CA、证书和私钥。如果轮换中的证书与私钥暂不匹配，设备会继续退避重试。中断时请求可能已进入调度器，重新连接不会自动重放。Go 0.23 会话接口尚未向监听端暴露远端 ID，也没有配置会话总出站队列上限的接口。[传输协议](../../link/grpc/wire/link.proto)
