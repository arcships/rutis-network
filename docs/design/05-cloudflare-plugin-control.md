# 用 Go 插件管理 Cloudflare Tunnel

[English](05-cloudflare-plugin-control.en.md)

建议先交付 **Access TCP + 命名隧道**。每条链路提供一个本地 rutis 控制服务，供其他插件启停、选目标和读状态。Go 插件托管官方 `cloudflared` 子进程，独占一个 Go TCP 插件实例处理双向 TLS（mTLS）、分帧和会话。这样应用只需注入一个服务，连接中断时仍能控制它。[Cloudflare 路由协议][routing] · [TCP 插件控制接口][tcp]

这是 2026-11-26 的设计建议。`go:link-cloudflared` 和下文的 Cloudflare 配置字段均待实现。原设计 PR #15 基于 rutis 0.21 / 协议 4。本文以 PR #16 的 Go 实现为依赖，目标是两端都运行 rutis 0.23.0 / 协议 5。两个版本不能互通。[Cloudflare 草案][draft] · [TCP 实现 PR][tcp-pr] · [rutis 迁移说明][migration]

## 先让两台机器上的插件互相调用

假设 A 是笔记本，运行 `consumer`，B 是办公室机器，运行 `greeter`。可以直接使用 rutis 0.23.0 自带的 `rutis-host/dial` 和 `rutis-host/listen`。Cloudflare 承载两台宿主之间的一条连接，连接上的普通服务双向可见。下面用 A 调用 B 举例。[rutis 节点与服务](https://github.com/arcships/rutis/blob/v0.23.0/docs/guide/nodes.md)

**B 端的 `rutis.json`**。将提供 `greeter` 服务的 Go 插件二进制放在配置文件旁的 `plugins/go`。插件在 `Apply` 中调用 `row.Provide("greeter", &Greeter{})`，其导出方法 `Hello(name string) string` 返回问候语。

```json
{
  "id": "office",
  "runtimes": { "go": { "dir": "plugins/go" } },
  "listen": [{ "name": "local", "address": "127.0.0.1:7443" }],
  "rows": [
    { "id": "laptop", "name": "rutis-host/listen", "config": { "listener": "local" } },
    { "id": "greeter", "name": "go:greeter" }
  ]
}
```

**A 端的 `rutis.json`**。将调用方 `consumer` 的 Go 插件二进制放在本机的 `plugins/go`。此处的 17443 是 A 的本地转发端口。

```json
{
  "id": "laptop",
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [
    { "id": "office", "name": "rutis-host/dial", "config": { "address": "ws://127.0.0.1:17443/rutis" } },
    { "id": "consumer", "name": "go:consumer" }
  ]
}
```

**接通链路。** 两端各自给 `rutis-host` 配同一份随机连接密钥：B 的环境变量是 `RUTIS_TOKEN_LAPTOP`，A 的是 `RUTIS_TOKEN_OFFICE`，后缀来自各自的连接行 ID。Cloudflare 使用独立凭据。[rutis 节点认证](https://github.com/arcships/rutis/blob/v0.23.0/docs/guide/nodes.md)

1. 在 Cloudflare 为 `rutis.example.com` 配置 self-hosted Access 应用和指定机器令牌的 Service Auth 策略。在命名隧道中添加同一主机名的 TCP 路由，目标为 `tcp://127.0.0.1:7443`，并将代理 DNS 指向该隧道。[路由][routing] · [Access 服务令牌][access]
2. B 启动 `rutis-host run rutis.json`，另起进程运行 `cloudflared tunnel run --token-file /run/secrets/tunnel-token`。[connector 参数][run]
3. A 将 Access 的 Client ID 和 Secret 分别通过 `TUNNEL_SERVICE_TOKEN_ID`、`TUNNEL_SERVICE_TOKEN_SECRET` 提供给 `cloudflared access tcp --hostname rutis.example.com --url 127.0.0.1:17443`。保持它运行，再启动 A 的 `rutis-host run rutis.json`。[Access 客户端][cli]

**业务插件的完整 Go 示例。** 将下面代码保存为 `main.go`。同一份程序注册两个插件，A 和 B 各自的配置决定加载哪一个。

```go
package main

import (
    "context"
    "github.com/arcships/rutis/go/rutis"
)

type Greeter struct{}

func (*Greeter) Hello(name string) string { return "office: hello " + name }

var GreeterPlugin = rutis.Define(rutis.Plugin[rutis.NoConfig]{
    Name: "greeter",
    Apply: func(row *rutis.Ctx, _ rutis.NoConfig) error {
        row.Provide("greeter", &Greeter{})
        return nil
    },
})

type GreeterRef struct {
    Hello func(context.Context, string) (string, error)
}

type Consumer struct{ greeter GreeterRef }

func (c *Consumer) Greet(ctx context.Context, name string) (string, error) {
    return c.greeter.Hello(ctx, name)
}

var ConsumerPlugin = rutis.Define(rutis.Plugin[rutis.NoConfig]{
    Name:   "consumer",
    Inject: []string{"greeter"},
    Apply: func(row *rutis.Ctx, _ rutis.NoConfig) error {
        var g GreeterRef
        if err := row.Use("greeter", &g); err != nil {
            return err
        }
        row.Provide("consumer", &Consumer{greeter: g})
        return nil
    },
})

func main() { rutis.Serve(GreeterPlugin, ConsumerPlugin) }
```

在空目录执行以下命令。将生成的 `business` 放入两端配置所指的目录。两端平台不同时分别为目标平台构建。

```sh
go mod init example.com/rutis-cloudflare-demo
go get github.com/arcships/rutis/go/rutis@v0.23.0
mkdir -p plugins/go
go build -o plugins/go/business .
```

在 A 上执行 `rutis-host call --config rutis.json consumer.greet '"Rutis"'`。若 B 的方法返回 `"office: hello " + name`，A 会收到 `"office: hello Rutis"`。已用两个真实 rutis 宿主、Go 业务插件和真实的 `cloudflared access tcp` 在回环环境取得这个结果，Cloudflare 边缘及其回源由本地测试服务模拟。这里使用内置 WebSocket 的连接密钥，Cloudflare 终止外层 TLS。下文给出需要由插件管理子进程和端到端 mTLS 时的配置。

## 三种接入方式

| 方式 | 拨号端和回源配置 | 适用场景 |
| --- | --- | --- |
| Access TCP（首版） | 两端安装 `cloudflared`。拨号侧将回环 TCP 经 WebSocket 发往 Access。源站 connector 向 Cloudflare 出站，转发到 `tcp://127.0.0.1:7443`。 | 无人值守的桌面或服务器，源站在 NAT 后，拨号侧不能装 VPN。可复用 #16 的 TCP 插件。 |
| 原生 WSS（可选） | 客户端直接连 `wss://<hostname>`。ingress 指向能升级 WebSocket 的 HTTP(S) 源站，复用 rutis WebSocket 传输。每条二进制消息保留一个消息边界。 | 浏览器或不能安装子进程的客户端。浏览器先在同一主机名完成 Access 登录，握手携带适用的 cookie。前端不分发机器令牌。原生 Go 客户端可在升级请求携带服务令牌头。 |
| WARP / Client-to-Tunnel | 受管设备注册 Cloudflare One Client，配置私网路由、Split Tunnel 和访问策略。Go 拨号端连可路由的私网服务地址。若监听器只绑定回环，需另设可达入口。 | 设备可纳管、重视长连接时优先评估。Access TCP 和原生 WSS 都可能被边缘重启中断。 |

首版的 Service Auth 策略只允许指定的机器令牌。WebSocket 外层 TLS 终止于 Cloudflare。内层 mTLS 一直通到 TCP 监听插件，也保护两端回环端口。两端随后按 #16 的 4 字节大端长度分帧，每帧上限 16 MiB。失败的准入产生零个 rutis 会话。远端插件名中的 ID 来自 rutis Hello。[Access 服务令牌][access] · [WebSocket 行为][ws] · [浏览器 WebSocket 接口][browser] · [Split Tunnel][warp]

## 部署与配置

下例是首版拟议的拨号宿主配置。部署者把两个 Go 插件二进制放到 `plugins/go`，在宿主安装 `cloudflared`。每个 profile 固定绑定公网主机名、源站证书名和凭据引用。运行时只能选择已声明的 profile，以免把现有令牌发给任意输入的域名。

```json
{
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

在源站宿主沿用相同的 Go 运行时，换成下面两行。例中的 7443 也必须与远程 ingress 配置一致。客户端校验证书名 `origin.internal`，监听端凭所配 CA 校验客户端证书。[TCP 证书与监听配置][tcp-guide]

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
| `configure({profile})` | 拨号侧原子校验后切换。相同配置幂等，非法配置保留旧连接。运行中切换会先断开旧链路。已停止时只保存选择。运行时选择保存在内存，重载从行配置恢复。 |
| `start()` | 幂等，返回表示已接受请求。拨号侧启动 `cloudflared access tcp --hostname <hostname> --url 127.0.0.1:<forwardPort>`，配置并启动 TCP 行（回环主机、转发端口、源站证书名）。源站先配置并启动 TCP 监听，确认绑定后启动 `cloudflared tunnel run --token-file <path>`。 |
| `stop()` | 幂等。取消本行重试，停掉独占的 TCP 实例及会话，终止并等待本行子进程退出，再返回。保留行和远程资源。 |
| `status()`、`waitForChange(cursor)` | status 立即返回快照。waitForChange 在游标已过期时立即返回，否则等待变化，允许取消。字段包括 `role`、`desired`、`profile`（拨号侧）、`process`、`tcpPhase`、`lastError`、`retryAt`、不透明 `cursor`。connector 的 `edgeReady` 取自回环 metrics 的 `/ready`，未知时返回 null。 |

把子进程监管和会话重拨分开。Go 用 `os/exec` 直接传参数并回收子进程，串行处理 `configure/start/stop`，每行注册一次卸载清理。子进程退出才由隧道插件退避重启。子进程存活时的 TCP 断流由 #16 重拨，新的会话不重放旧调用。启动失败（例如缺少二进制、端口占用）报告所在阶段并停止。转发端口由部署分配给该实例。在共享宿主上须确认监听 socket 归属本行子进程后再启动 TCP 行。[cloudflared 转发实现][carrier] · [TCP 重连策略][tcp]

拨号子进程启动时从文件读凭据，仅在该子进程环境中设置 `TUNNEL_SERVICE_TOKEN_ID` 和 `TUNNEL_SERVICE_TOKEN_SECRET`。connector 只接收 tunnel token 文件。使用显式环境，避免继承覆盖 token 文件的 `TUNNEL_TOKEN`。轮换后重启对应子进程重新读取。状态、RPC 和日志均只给出脱敏结果。保持普通日志级别，所查版本的 debug 会转储含请求头的请求。[CLI 参数与请求头][cli] · [debug 实现][debug] · [token 文件参数][run]

**状态的判据。** 回环端口可连只说明本地有监听器。`/ready` 成功说明 connector 到边缘有连接。TCP 插件的 `connected` 表示本端 mTLS 已通过且已交给会话服务。业务可用以远端目标服务注册或一次成功调用为准。Access 日志可确认策略拒绝。若本地只观测到 EOF，就记录传输中断，保留原因未知。0.23 的 Go 会话接口没有握手后的远端 ID 和会话出站队列上限。需要同 ID 监听接替或硬性出站内存上限时，分别使用支持握手接替或显式设置上限的 Rust bridge。[connector 就绪实现][ready] · [rutis 会话接口与限制][tcp-guide]

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
- **实现验收**。用本地假进程和两个真实 rutis 宿主覆盖正常调用、Access 拒绝或 mTLS 失败时零会话、端口占用、令牌轮换、并发启停、重载后无遗留进程。从远端确认无法调用管理服务。在接口层检查日志和快照没有测试密钥。
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
