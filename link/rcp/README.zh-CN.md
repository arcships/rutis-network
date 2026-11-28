# RCP 设备连接插件

[English](README.md)

`go:link-rcp` 每个配置行连接一台设备。两端可以分别拨入 WebSocket relay，也可以由客户端直接拨入宿主监听地址。Go 运行时为多行共用一个插件进程，每行独立管理连接、session、凭证回调和退避。进程随宿主启停。撤销在线设备时，卸载对应行并在签发端撤销授权。

## 配置

在本目录运行 `go build -o /path/to/project/plugins/go/link-rcp ./cmd/link-rcp`。宿主项目的 `rutis.json` 配置如下。

```json
{
  "id": "desk",
  "runtimes": { "go": { "dir": "./plugins/go" } },
  "rows": [
    {
      "id": "tablet",
      "name": "go:link-rcp",
      "config": { "linkId": "tablet", "credentialFile": "/run/rcp/tablet.json" }
    }
  ]
}
```

独立插件在每次建连时重新读取调用方的凭证文件。调用方应原子替换文件，并限制普通文件仅当前用户可读写（Unix 下 0600，Windows 使用对应 ACL）。嵌入 Go 定义时也可向 `rcp.Plugin` 传入本地 `Provider`，回调按 `linkId` 取凭证。网络超时会退避重试。签发服务返回 HTTP 503 等临时故障时，回调应包装 `rcp.ErrProviderUnavailable`。直连监听端在此期间返回 HTTP 503。其余回调错误按凭证拒绝处理。

| 凭证字段 | 调用方提供的值 |
| --- | --- |
| `role` | `client` 发起配对，`daemon` 响应 |
| `address` | 完整拨号 URL，含 relay 所需的短期票据，直连监听端可省略。WSS 使用系统信任根。明文 WS 仅允许数字回环地址。 |
| `privateKey`、`peerKey` | 本地 X25519 私钥和经独立验证的对端公钥，各 32 字节，Base64 编码 |
| `binding` | 此次授权中双方共享的非空字节串，最多 1024 字节，Base64 编码 |
| `expiresAt` | RFC 3339 格式的准入截止时间。完成配对时凭证必须有效，已建立的会话随配置行管理。 |

直连时在宿主行设置 `"listen": "wss://0.0.0.0:7443/pair"`，用 `cert`、`key` 指定 TLS 证书和私钥文件。客户端凭证填写实际可达地址。本机测试可使用 `ws://127.0.0.1:7443/pair`。relay 模式下，签发端分别提供两个角色的完整拨号地址。中继应能承载 60 字节握手消息，以及配置上限加 33 字节的加密记录。

`maxMessage` 默认 16 MiB，`heartbeatMs` 默认 10000。插件串行写 socket，等待上一个 session 接收完成才读下一条。rutis 0.23.0 的 Go 接口无法限制宿主 session 的独立发送队列，也无法把协商的单帧上限传给分块器。超限 RPC 会使当前连接以 `frame-too-large` 结束。宿主测试让双方分别返回 17 MiB，随后验证小调用恢复、另一台设备持续可用。[上游问题](https://github.com/arcships/rutis/issues/1411)

诊断字段为 link ID、方向、阶段和固定原因码，link ID 使用适合写日志的标识。传输错误、对端离线或重新配对、消息超限、relay 或凭证服务暂时不可用会触发退避重连，从 250 ms 开始倍增，最长 30 s。凭证拒绝或过期允许刷新一次。版本不符、帧格式错误、被替换、session 拒绝时结束该行的拨号任务，修正后重新加载该行。本地主动关闭 session 时即使最后的通知发送失败，也会停止重拨。配对成功后退避复位。

## Android 和 iOS

[Kotlin 模块](kotlin) 支持 Android API 29+ 和 JVM 17，[Swift 包](swift) 支持 iOS 15+ 和 macOS 12+。两端实现 RCP/1，测试直接读取 Go 的共享向量。Android 安装 JDK 17 和 Android SDK 后，运行 `cd link/rcp/kotlin && ./gradlew -Pandroid=true :android:assembleRelease` 构建 AAR，在应用中使用带所声明依赖的 Android Gradle 模块。iOS 在 Xcode 中添加本地 Swift 包 `link/rcp/swift`，选择产品 `RutisRCP`。

Kotlin 为每台设备启动一个协程，调用 `runRcp(provider = { freshGrant() }) { sender -> receiverFor(sender) }`。Swift 为每台设备启动一个 Task，调用 `try await RCPLink.run(provider: { try await freshGrant() }) { sender in try await receiverFor(sender) }`。两个回调由应用提供。每次拨号前，凭证回调按上表返回最新授权。双方验证 READY 后，第二个回调取得 sender，返回 Kotlin 的 `Receiver` 或 Swift 的 `RCPReceiver`。等待逐条发送和接收可保持消息顺序并施加背压。接收器的结束回调取得关闭原因。取消协程或 Task 可卸载连接，本地主动关闭 sender 也会停止重拨。可选的重试回调提供公开原因和退避毫秒数。

直连填写 daemon 地址，中继填写对应角色的地址。除数字回环地址外均要求 WSS。应用已有 WebSocket 监听器时，可以将升级后的二进制 socket 传入 `serveRcp` 或 `RCPLink.serve`。适配器应在缓冲消息前应用读取上限，并在关闭时中断正在等待的 I/O。这些 API 交付有边界的消息通道。挂载到移动 rutis 宿主并调用服务，还需[上游 SDK 公开接口 #1412](https://github.com/arcships/rutis/issues/1412)。

在安装 JDK 17 和 Xcode 的 macOS 上，从仓库根目录运行 `make integration-rcp-mobile`。命令构建两端测试程序并校验 Go 共享向量，分别测试 Go↔Kotlin、Go↔Swift、Kotlin↔Swift 的直连和 relay。双方各发送一条不同的消息并核对确认回复。测试还覆盖两种移动 daemon、拒绝与重试、心跳失联、慢消费者、取消和消息大小边界。在 Kotlin 模块内运行 `./gradlew -Pandroid=true :android:connectedDebugAndroidTest` 可在连接的 Android 测试设备上运行向量。

## 验证

在本目录运行 `go test -race ./...`，覆盖向量、准入、故障映射、顺序、上限、背压和刷新。在仓库根目录运行 `npm ci --prefix link/tcp/node` 和 `make integration-rcp`，使用三台真实宿主、两行设备、直连和本地 relay 验证双向调用与密文抓取。测试会生成所需凭证。

在仓库根目录运行 `make integration-rcp-direct` 或 `make integration-rcp-relay` 可分别验收 Go 直连和 relay。CI 为两条路径分别显示结果。只跑一个场景时，在本目录运行 `go test -race -v -run '^TestGoFaultMatrix$/^relay$/^peer_offline$' .`。

| 故障用例 | 预期拨号行为 |
| --- | --- |
| `version_mismatch` | 对端拒绝版本后停止拨号，创建的会话数为零。 |
| `invalid_grant`、`revoked_grant` | 凭证绑定不匹配或收到 HTTP 403 时刷新一次，再次拒绝则停止该行拨号。 |
| `unavailable_503` | 收到 HTTP 503 后重试，恢复后在新会话上完成消息往返。 |
| `peer_offline` | 直连地址停止监听时报 `transport-error`，relay 返回 HTTP 404 时报 `peer-offline`。两条路径都应重试，恢复后完成消息往返。 |

`go test -race -run '^TestDialBackoff$' .` 使用虚拟时钟逐次检查从 250 ms 到 30 s 上限的退避、会话结束后的复位，以及等待重试期间的取消。`make integration-rcp-hosts` 还会在退避期间卸载真实宿主的配置行，检查凭证请求随之停止。

[线协议与向量](wire.zh-CN.md)
