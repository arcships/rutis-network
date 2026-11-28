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

独立插件在每次建连时重新读取调用方的凭证文件。调用方应原子替换文件，并限制普通文件仅当前用户可读写（Unix 下 0600，Windows 使用对应 ACL）。嵌入 Go 定义时也可向 `rcp.Plugin` 传入本地 `Provider`，回调按 `linkId` 取凭证。

| 凭证字段 | 调用方提供的值 |
| --- | --- |
| `role` | `client` 发起配对，`daemon` 响应 |
| `address` | 完整拨号 URL，含 relay 所需的短期票据，直连监听端可省略。WSS 使用系统信任根。明文 WS 仅允许数字回环地址。 |
| `privateKey`、`peerKey` | 本地 X25519 私钥和经独立验证的对端公钥，各 32 字节，Base64 编码 |
| `binding` | 此次授权中双方共享的非空字节串，最多 1024 字节，Base64 编码 |
| `expiresAt` | RFC 3339 格式的准入截止时间。完成配对时凭证必须有效，已建立的会话随配置行管理。 |

直连时在宿主行设置 `"listen": "wss://0.0.0.0:7443/pair"`，用 `cert`、`key` 指定 TLS 证书和私钥文件。客户端凭证填写实际可达地址。本机测试可使用 `ws://127.0.0.1:7443/pair`。relay 模式下，签发端分别提供两个角色的完整拨号地址。中继应能承载 60 字节握手消息，以及配置上限加 33 字节的加密记录。

`maxMessage` 默认 16 MiB，`heartbeatMs` 默认 10000。插件串行写 socket，等待上一个 session 接收完成才读下一条。rutis 0.23.0 的 Go 接口无法限制宿主 session 的独立发送队列，也无法把协商的单帧上限传给分块器。超限 RPC 会使当前连接以 `frame-too-large` 结束。宿主测试覆盖 17 MiB 返回值引发断线、随后重连，以及另一台设备持续可用。[上游问题](https://github.com/arcships/rutis/issues/1411)

诊断字段为 link ID、方向、阶段和固定原因码，link ID 使用适合写日志的标识。传输错误、对端离线、relay 不可用会触发退避重连，从 250 ms 开始倍增，最长 30 s。凭证拒绝或过期允许刷新一次。版本或帧错误、被替换、session 拒绝时结束该行的拨号任务，修正后重新加载该行。配对成功后退避复位。

## 验证

在本目录运行 `go test -race ./...`，覆盖向量、准入、故障映射、顺序、上限、背压和刷新。在仓库根目录运行 `npm ci --prefix link/tcp/node` 和 `make integration-rcp`，使用三台真实宿主、两行设备、直连和本地 relay 验证双向调用与密文抓取。测试会生成所需凭证。

[线协议与向量](wire.zh-CN.md)
