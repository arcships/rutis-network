# TCP 字节流 bridge 与反向隧道

2026-11-26 定稿。基线为 rutis 0.23.0（会话协议 5，插件 API 3）。实现范围与验收记录在 [issue #6](https://github.com/arcships/rutis-network/issues/6)。[English](06-link-tcp.en.md)

## 边界与帧格式

SSH 转发、frp、Tailscale、Cloudflare Access、ADB 和 usbmuxd 都可以提供字节流。adapter 负责到达与准入，每条已验证的流交给 Go 的 `tcp.Open`。Go bridge 遵循 #11 的帧格式，与其 TypeScript 参考实现互通。bridge 负责分帧、背压、关闭和一次 `sessions.open(far)`。rutis 独自处理 Hello、远端身份和服务生命周期。已有 WebSocket 传输保留自己的消息边界。[链接插件契约](01-link-plugin-contract.md)

每帧由四字节无符号大端长度和指定字节数的载荷组成。长度范围含 0 和 16 MiB。超长在分配前拒绝。接收端等待 `near.receive` 完成再交付下一帧。发送端一次等待一个 socket 写入，无额外待发队列，4–16 MiB 的合法帧也可以直接发送。rutis 0.23 的会话出站队列与 bridge 各自独立，非 Rust 接口无法设置会话总限额。Rust 可用 `SessionOptions::outbound_limit` 设置字节预算。

帧边界 EOF 正常结束，半个头或半个载荷以 `truncated-frame` 失败，超长以 `frame-too-large` 失败，连接复位保留传输错误码。任何一方关闭都释放整个 socket。卸载中止等待中的读写。TCP 只承载字节与 EOF，WebSocket 的 4002/4003 关闭码留在 WebSocket 上。

在 rutis 0.23.0 上，大数据默认由应用分块传输，接收方按顺序重组。集成测试将 17 MiB 随机二进制数据分成四个 4 MiB 块和一个 1 MiB 尾块，在同一条连接上逐块回显。测试逐字节比对重组结果，再复用该连接调用服务。4 MiB 是测试选用的块大小，须为会话编码和元数据留出空间。

一次大调用的透明分块由会话协议 5 负责。0.23.0 的 `sessions.open(far)` 无法声明传输的单帧上限，编码后的整条消息超过 16 MiB 时，bridge 会关闭该连接。待上游发布支持后，插件将声明 16 MiB，并增加单次 17 MiB 调用成功的用例，验证各帧符合上限、完整值只交付一次且连接仍可用。[上游跟踪与对照实测](https://github.com/arcships/rutis/issues/1411)

## 拨号、监听与重连

- 两个 Go 插件固定使用 `127.0.0.1`。监听端可选端口 0 并报告实际端口。两个方向分别校验证书，只把通过本端双向 TLS 准入的 socket 交给 bridge。隔离的分帧测试可以使用原始回环 TCP。
- 拨号行保留 #11 单次拨号的默认行为。设置 `reconnect: true` 后，由 adapter 发起首次尝试，断开后新建 socket 和会话。旧帧不重放。瞬时网络错误从 0.5 秒指数退避至 30 秒，连接稳定 60 秒后重置。已知证书拒绝等待 30 秒；确定不兼容的传输协议停止重试。非 Rust 接口拿不到 rutis 握手结果，未知断开按传输错误处理。
- 监听行可同时接收多条连接，每条一个会话。握手和各次读取彼此独立；行卸载同时关闭监听器、正在做 TLS 握手的套接字和已准入的连接。监听资源错误经延迟后重试，避免空转。
- 同一行的受理句柄先到先得。非 Rust 的 `open` 没有握手后的远端 ID。需要按 ID 顶替的监听器使用 Rust 的 `Session::handshake`，待旧会话卸载后挂载新会话。[Rust 示例](../../examples/rust-listen/src/lib.rs)

## 反向隧道的信任边界

隧道工具由运维启动。它们提供从拨号端到监听端的 TCP 路由，使用同一份 bridge。frp/rathole 的 token、ngrok 的账户和 SSH 凭据约束谁能建立隧道；连接转发端口的来访方仍需端到端准入。示例绑定回环，并在隧道内保留双向 TLS。开放外部监听地址会扩大能接触端口的范围。

| 路由 | 接入和验收 |
| --- | --- |
| frp / rathole | 控制连接使用各自凭据；以真实 frpc/frps 的 TCP 转发作为本地验收。 |
| OpenSSH 反向转发 | 客户端校验宿主密钥，以用户密钥登录。以真实 SSH 反向转发验证回环监听端。 |
| ngrok TCP | 账户创建受管入口，地址、访问策略和配额由供应商管理。 |
| Cloudflare Access | 本地 TCP 由受控入口提供。[适配器设计](https://github.com/arcships/rutis-network/pull/15) |

## 验收

在回环上覆盖拆包、粘包、空帧、1,000 条消息顺序、16 MiB 边界、慢端、EOF/复位、独立会话、拒绝准入、重连与卸载。分别通过 frp 和 SSH 转发验证有边界的双向消息。Rust 示例验证按 ID 顶替，Rust 会话预算通过上限测试验证。执行命令和配置见[开发指南](../guide/link-plugins.md)。
