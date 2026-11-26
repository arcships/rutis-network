# 用 Go 插件管理 Cloudflare Tunnel

[English](05-cloudflare-plugin-control.en.md)

`go:link-cloudflared` 在 rutis 行里运行，托管 Cloudflare Access TCP 转发或源站 connector。两端使用 rutis 0.23.0（协议 5），安装 `cloudflared`（token 文件需要 2025.4.0 或更新版本）。云端使用预配置的命名隧道。默认接法直接使用 rutis 的 WebSocket 连接行，需要端到端 mTLS 时可接入 PR #16 的 TCP 控制服务。[Cloudflare 路由][routing] · [connector 参数][run] · [TCP 实现 PR][tcp-pr]

## 先让两台机器上的插件互相调用

在仓库根目录用 Go 1.24 或更新版本构建。按各自平台将生成的两个二进制部署到两端 `rutis.json` 旁的 `plugins/go`。示例中办公室 B 提供 `greeter.hello`，笔记本 A 上的 `consumer` 注入 `greeter` 并调用这个方法。[完整 Go 业务插件](../../link/cloudflared/example/business/main.go) · [rutis 节点与服务](https://github.com/arcships/rutis/blob/v0.23.0/docs/guide/nodes.md)

```sh
mkdir -p plugins/go
go -C link/cloudflared build -o ../../plugins/go/link-cloudflared ./cmd/link-cloudflared
go -C link/cloudflared build -o ../../plugins/go/business ./example/business
```

**办公室 B 的 `rutis.json`**

```json
{
  "id": "office",
  "runtimes": { "go": { "dir": "plugins/go" } },
  "listen": [{ "name": "local", "address": "127.0.0.1:7443" }],
  "rows": [
    { "id": "laptop", "name": "rutis-host/listen", "config": { "listener": "local" } },
    { "id": "greeter", "name": "go:greeter" },
    {
      "id": "origin", "name": "go:link-cloudflared",
      "config": { "role": "connector", "tunnelTokenFile": "/run/secrets/tunnel-token", "metricsPort": 20411 }
    }
  ]
}
```

**笔记本 A 的 `rutis.json`**

```json
{
  "id": "laptop",
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [
    {
      "id": "access", "name": "go:link-cloudflared",
      "isolate": { "office-link": "management" },
      "config": {
        "role": "dial", "forwardPort": 17443, "profile": "office", "controlService": "office-link",
        "profiles": { "office": {
          "hostname": "rutis.example.com", "clientIdFile": "/run/secrets/access-id",
          "clientSecretFile": "/run/secrets/access-secret"
        } }
      }
    },
    { "id": "office", "name": "rutis-host/dial", "config": { "address": "ws://127.0.0.1:17443/rutis" } },
    { "id": "consumer", "name": "go:consumer" }
  ]
}
```

1. 在 Cloudflare 配置 `rutis.example.com` 的 self-hosted Access 应用及指定机器令牌的 Service Auth 策略，再配置命名隧道的 TCP 路由到 `tcp://127.0.0.1:7443` 和对应代理 DNS。把 tunnel token 文件部署到 B，把 Access Client ID 和 Secret 的文件部署到 A。仅允许对应运行账户读取。[创建隧道][create] · [Access 服务令牌][access]
2. 给 B 的 `RUTIS_TOKEN_LAPTOP` 和 A 的 `RUTIS_TOKEN_OFFICE` 设置同一份随机连接密钥。后缀来自各自的连接行 ID。两端分别运行 `rutis-host run rutis.json`，源站先启动。Go 插件负责启动各自的 `cloudflared` 子进程。
3. 在 A 上执行 `rutis-host call --config rutis.json consumer.greet '"Rutis"'`。返回 `"office: hello Rutis"` 表示 A 的业务插件已调到 B。连接上的普通服务双向可见，将本地管理服务用 `isolate` 限定作用域。

该接法由 Cloudflare 终止外层 TLS，内层 WebSocket 使用 rutis 连接密钥准入。Go 转发器绑定本机 IPv4 回环并一次接受一个活动流。为避免其他本地用户占用这一流，将转发器部署在受信任的宿主或独立网络命名空间。停止 Cloudflare 行会关闭它的传输；独立的 `rutis-host/dial` 行仍按宿主的策略等待重连。

## 三种接入方式

| 方式 | 拨号端和回源配置 | 适用场景 |
| --- | --- | --- |
| Access TCP（首版） | 两端安装 `cloudflared`。拨号侧将回环 TCP 经 WebSocket 发往 Access。源站 connector 向 Cloudflare 出站，转发到 `tcp://127.0.0.1:7443`。 | 无人值守的桌面或服务器，源站在 NAT 后，拨号侧不能装 VPN。可复用 #16 的 TCP 插件。 |
| 原生 WSS（可选） | 客户端直接连 `wss://<hostname>`。ingress 指向能升级 WebSocket 的 HTTP(S) 源站，复用 rutis WebSocket 传输。每条二进制消息保留一个消息边界。 | 浏览器或不能安装子进程的客户端。浏览器先在同一主机名完成 Access 登录，握手携带适用的 cookie。前端不分发机器令牌。原生 Go 客户端可在升级请求携带服务令牌头。 |
| WARP / Client-to-Tunnel | 受管设备注册 Cloudflare One Client，配置私网路由、Split Tunnel 和访问策略。Go 拨号端连可路由的私网服务地址。若监听器只绑定回环，需另设可达入口。 | 设备可纳管、重视长连接时优先评估。Access TCP 和原生 WSS 都可能被边缘重启中断。 |

首版的 Service Auth 策略只允许指定的机器令牌。WebSocket 外层 TLS 终止于 Cloudflare。使用可选 TCP 插件时，内层 mTLS 一直通到 TCP 监听插件，也保护两端回环端口。两端随后按 #16 的 4 字节大端长度分帧，每帧上限 16 MiB。失败的准入产生零个 rutis 会话。远端插件名中的 ID 来自 rutis Hello。[Access 服务令牌][access] · [WebSocket 行为][ws] · [浏览器 WebSocket 接口][browser] · [Split Tunnel][warp]

## 部署与配置

如需 mTLS，将 PR #16 的 Go TCP 插件二进制也安装到两端的 `plugins/go`。以下配置在两端分别为隧道分配专用 TCP 行。每个 profile 固定绑定公网主机名、源站证书名和凭据引用。运行时只能选择已声明的 profile，以免把现有令牌发给任意输入的域名。

```json
{
  "id": "laptop",
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [
    {
      "id": "wire",
      "name": "go:link-tcp-dial",
      "isolate": { "office-tcp": "office-internal" },
      "config": {
        "controlService": "office-tcp", "autoStart": false, "reconnect": true,
        "ca": "/etc/rutis/pki/ca.pem", "cert": "/etc/rutis/pki/client.pem", "key": "/etc/rutis/pki/client.key"
      }
    },
    {
      "id": "office-tunnel",
      "name": "go:link-cloudflared",
      "inject": ["office-tcp"],
      "isolate": { "office-tcp": "office-internal", "office-link": "office-management" },
      "config": {
        "role": "dial", "tcpService": "office-tcp", "controlService": "office-link",
        "autoStart": false, "forwardPort": 17443, "profile": "lab",
        "profiles": {
          "lab": {
            "hostname": "rutis.example.com", "servername": "origin.internal",
            "clientIdFile": "/run/secrets/access-id", "clientSecretFile": "/run/secrets/access-secret"
          }
        }
      }
    }
  ]
}
```

在源站宿主设置 `"id": "office"`，沿用相同的 Go 运行时，换成下面两行。例中的 7443 也必须与远程 ingress 配置一致。客户端校验证书名 `origin.internal`，监听端凭所配 CA 校验客户端证书。[TCP 证书与监听配置][tcp-guide]

```json
[
  {
    "id": "wire", "name": "go:link-tcp-listen",
    "isolate": { "origin-tcp": "origin-internal" },
    "config": {
      "controlService": "origin-tcp", "autoStart": false,
      "ca": "/etc/rutis/pki/ca.pem", "cert": "/etc/rutis/pki/server.pem", "key": "/etc/rutis/pki/server.key"
    }
  },
  {
    "id": "office-tunnel", "name": "go:link-cloudflared",
    "inject": ["origin-tcp"],
    "isolate": { "origin-tcp": "origin-internal", "office-origin": "origin-management" },
    "config": {
      "role": "connector", "tcpService": "origin-tcp", "controlService": "office-origin",
      "autoStart": true, "originPort": 7443, "tunnelTokenFile": "/run/secrets/tunnel-token"
    }
  }
]
```

本地调用方在自己的行声明 `"inject": ["office-link"]` 和 `"isolate": {"office-link":"office-management"}`。Go 调用方用 `ctx.Use("office-link", &control)` 绑定方法，其他语言用各自的 SDK。原始 TCP 控制服务只给隧道插件使用。隔离很重要，因为宿主会把根作用域中的普通服务导出给远端。要在远端运行业务插件，行名写作 `wire:<plugin>`，会话挂在调用 `rutis/sessions.open` 的 TCP 行下。需要动态挂载时，可由本地插件注入 `rutis/loader`，在预设隔离组中创建行。[宿主导出规则][exports] · [动态挂载示例][tcp]

```go
func startLab(callCtx context.Context, row *rutis.Ctx) error {
    var link struct {
        Configure func(context.Context, map[string]any) error
        Start     func(context.Context) error
    }
    if err := row.Use("office-link", &link); err != nil {
        return err
    }
    if err := link.Configure(callCtx, map[string]any{"profile": "lab"}); err != nil {
        return err
    }
    return link.Start(callCtx)
}
```

## 插件控制契约

| 方法 | 调用方可以依赖的行为 |
| --- | --- |
| `configure({profile})` | 拨号侧先读取并校验目标 profile 的两个 Access 凭据文件，再切换。相同配置幂等，非法配置保留旧连接。运行中切换会先断开旧链路。已停止时只保存选择。运行时选择保存在内存，重载从行配置恢复。 |
| `start()` | 幂等，返回表示已接受请求。Go 在拨号端绑定回环端口，每条流用 `cloudflared access tcp --hostname <hostname>` 的标准输入输出承载。配置了专用 TCP 行时，该行拨打本地端口并校验源站证书名。源站先配置并启动 TCP 监听，确认绑定后启动 `cloudflared tunnel run --token-file <path>`。 |
| `stop()` | 幂等。取消本行重试，停掉独占的 TCP 实例及会话，终止并等待本行子进程退出，再返回。保留行和远程资源。 |
| `status()`、`waitForChange(cursor)` | status 立即返回快照。waitForChange 在游标已过期时立即返回，否则等待变化，允许取消。字段包括 `role`、`desired`、`profile`（拨号侧）、`process`、`tcpPhase`、`lastError`、`retryAt`、不透明 `cursor`。源站可用 `metricsPort` 指定独占的回环端口探测 `/ready`。未配置或结果未知时 `edgeReady` 为 null。可选 TCP 实例提供 `tcpPhase`、`tcpError` 和 `tcpRetryAt`。 |

源站 connector 在 token 文件不可读或格式异常（`invalid-credentials`）、指标端口绑定失败（`metrics-bind-failed`）或子进程退出后按 0.5–30 秒退避重试。每次尝试都会重读 token 文件，首次挂载延迟也能自动恢复。持续运行 60 秒后重置退避时间。拨号端由会话适配器重拨，每条新连接启动独立的子进程并重新读取凭据。任一方向 EOF 或取消都会释放 socket 和两条管道。缺少二进制报 `binary-unavailable`，绑定冲突报 `address-in-use`。可在部署配置的 `binary` 指定程序路径。使用专用 TCP 行时，转发端口填 0 可自动分配，实际端口见 `boundPort`。使用内置拨号行时，两行填写相同的固定端口。[cloudflared 转发实现][carrier] · [TCP 重连策略][tcp]

拨号子进程启动时从文件读凭据，仅在该子进程环境中设置 `TUNNEL_SERVICE_TOKEN_ID` 和 `TUNNEL_SERVICE_TOKEN_SECRET`。connector 使用本行的 `tunnelTokenFile`。启动参数 `--config=` 关闭宿主机 YAML 配置发现，子进程环境中也排除 `TUNNEL_TOKEN`。轮换后重启对应子进程重新读取。状态、RPC 和日志均只给出脱敏结果。保持普通日志级别，所查版本的 debug 会转储含请求头的请求。[CLI 参数与请求头][cli] · [debug 实现][debug] · [token 文件参数][run]

**状态的判据。** 回环端口可连只说明本地有监听器。插件核对 `/ready` 返回的 `connectorId` 和当前子进程报告的 ID。匹配时，200 表示已连上边缘，503 表示已断开。无法确认时 `edgeReady` 为 null。TCP 插件的 `connected` 表示本端 mTLS 已通过且已交给会话服务。业务可用以远端目标服务注册或一次成功调用为准。Access 日志可确认策略拒绝。若本地只观测到 EOF，就记录传输中断，保留原因未知。0.23 的 Go 会话接口没有握手后的远端 ID 和会话出站队列上限。需要同 ID 监听接替或硬性出站内存上限时，分别使用支持握手接替或显式设置上限的 Rust bridge。[connector 就绪实现][ready] · [rutis 会话接口与限制][tcp-guide]

## 云端资源由谁配置

首版使用预配置资源，正常启停只需要两端各自的运行凭据。管理员在 Cloudflare 托管的 zone 上为同一精确主机名建立 self-hosted Access 应用，并绑定指定 service token 的 Service Auth 策略。创建远程管理的命名隧道（`config_src: "cloudflare"`），配置以下 ingress，最后发布代理 CNAME `rutis.example.com → <tunnel-id>.cfargotunnel.com`。这样在策略就位后才开放入口。[官方 API 建隧道流程][create] · [Access 策略与令牌][access]

```json
{
  "config": {
    "ingress": [
      { "hostname": "rutis.example.com", "service": "tcp://127.0.0.1:7443" },
      { "service": "http_status:404" }
    ]
  }
}
```

若要插件也负责开通云资源，建议增加**仅部署在管理宿主的独立 Go 插件**。配置给出 `accountId`、`zoneId`、专用 tunnel ID 或创建名、hostname、origin、现有 Access 策略 ID 和管理 API token 文件。控制服务提供 `plan()` 返回差异，`apply(planId)` 重读资源并应用仍有效的计划。Go SDK 可管理 tunnel、config、Access 和 DNS。只给该账户的 tunnel、Access 应用及目标 zone 的 DNS 写权限，复用既有策略和 service token。所查 `cloudflare-go/v7@v7.15.0` 需要 Go 1.26，单独的 Go module 可让运行端继续使用 #16 的 Go 1.24 基线。[Cloudflare API][api] · [Access 应用 API][apps] · [Go SDK 版本要求][sdk]

远程 ingress 更新是整份配置的 PUT。管理插件只写自己独占的隧道，按顺序确认 Access、ingress、DNS，记录资源 ID 和配置版本。部分成功后重读并复用已创建的资源。发现同名的非托管资源或配置漂移则返回冲突。回滚先撤下本次新建的 DNS 入口，再恢复本次变更的 ingress，最后处理本次创建的专用资源。由 Dashboard 或 Terraform 管理的资源继续由原管理方写入。[隧道配置 API][api]

## 验证结果与验收

- **本地已测**（macOS，`cloudflared 2026.11.1`）。把 `access tcp` 的上游指到回环测试服务，令它在收到 TLS ClientHello 时关闭。连续两次本地 TCP 连接均成功建立，随后读到 EOF。同一 `cloudflared` 进程始终存活，终止后释放转发端口。这个实验验证了状态判据和两层重试的分工。
- **自动化验证**覆盖二进制字节流、端口占用、凭据轮换、profile 切换、源站进程重试和并发控制。双宿主测试使用真实 Access TCP 客户端、本地模拟边缘及源站 connector 测试进程。无效凭据时业务不可达，轮换后可调用 Go 服务。测试还验证管理服务隔离、仅卸载及重新挂载拨号行后的服务恢复，分别运行内置 WebSocket 和 #16 mTLS 配置。[双宿主测试](../../link/cloudflared/hosts_test.go)
- **真实账户待测**。在测试 hostname 上依次用有效和撤销的凭据建新连接，核对 Access 日志和远端服务调用。随后中断 connector，检查旧会话消失、恢复后能再次调用，再执行 `stop()` 检查云端资源仍在。此次调研未进行公网端到端验证。

[routing]: https://developers.cloudflare.com/tunnel/concepts/routing/
[draft]: https://github.com/arcships/rutis-network/pull/15
[tcp-pr]: https://github.com/arcships/rutis-network/pull/16
[access]: https://developers.cloudflare.com/cloudflare-one/access-controls/service-credentials/service-tokens/
[ws]: https://developers.cloudflare.com/network/websockets/
[browser]: https://websockets.spec.whatwg.org/#the-websocket-interface
[warp]: https://developers.cloudflare.com/cloudflare-one/team-and-resources/devices/cloudflare-one-client/configure/route-traffic/split-tunnels/
[exports]: https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/session/plugin/services.rs
[carrier]: https://github.com/cloudflare/cloudflared/blob/2026.11.1/carrier/carrier.go
[cli]: https://github.com/cloudflare/cloudflared/blob/2026.11.1/cmd/cloudflared/access/carrier.go
[debug]: https://github.com/cloudflare/cloudflared/blob/2026.11.1/carrier/websocket.go
[run]: https://developers.cloudflare.com/tunnel/reference/run-parameters/#token-file
[ready]: https://github.com/cloudflare/cloudflared/blob/2026.11.1/metrics/readiness.go
[create]: https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/create-remote-tunnel-api/
[api]: https://developers.cloudflare.com/api/go/resources/zero_trust/subresources/tunnels/subresources/cloudflared/
[apps]: https://developers.cloudflare.com/api/resources/zero_trust/subresources/access/subresources/applications/methods/create/
[sdk]: https://github.com/cloudflare/cloudflare-go/blob/v7.15.0/go.mod
[tcp]: https://github.com/arcships/rutis-network/blob/eaa4c1ba1ffc4cd961d863f23a7bb4b5aaeae396/docs/design/06-link-tcp-integration.md
[tcp-guide]: https://github.com/arcships/rutis-network/blob/eaa4c1ba1ffc4cd961d863f23a7bb4b5aaeae396/docs/guide/link-plugins.md
[migration]: https://github.com/arcships/rutis/blob/v0.23.0/docs/migration-0.23.md
