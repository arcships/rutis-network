# TCP 插件的连接管理接口

集成方可以在本机改地址、连接、断开和观察状态。每个 Go 拨号或监听实例按部署配置提供一个 rutis 服务，随该实例存活。即使对端离线，管理插件也能调用它。适用于 rutis 0.23.0。[English](06-link-tcp-integration.en.md) · [构建和部署](../guide/link-plugins.md)

## 接入管理插件

在已有的 Go 和管理插件运行时配置中加入下面两行。将 `./plugins/settings.mjs` 换成自己的管理插件入口。第二条链路可以用另一个 TCP 实例和另一个服务名。

```json
{
  "rows": [
    {
      "id": "wire",
      "name": "go:link-tcp-dial",
      "isolate": { "office-tcp": "management" },
      "config": {
        "controlService": "office-tcp",
        "ca": "/etc/link/peer-ca.pem",
        "cert": "/etc/link/client.pem",
        "key": "/etc/link/client.key"
      }
    },
    {
      "id": "settings",
      "name": "./plugins/settings.mjs",
      "inject": ["office-tcp"],
      "isolate": { "office-tcp": "management" }
    }
  ]
}
```

**两行都要配置相同的服务名与 `isolate` 标签。** rutis 会向已连接的对端发布根作用域中的普通服务。提供方省略 `isolate` 会把连接控制权暴露给对端。隔离由宿主执行，管理插件通过 `inject` 获取服务，业务插件继续按服务名调用业务。两端宿主的隔离与远端业务调用在同一个测试中验证。[宿主导出规则](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/session/plugin/services.rs) · [集成测试](../../link/tcp/test/control.test.mjs)

在管理插件中可以这样响应“连接到服务器”和“断开”：

```js
const tcp = ctx.use('office-tcp')
await tcp.configure({ host: 'office.example.com', port: 7443 })
let state = await tcp.status()
while (state.phase !== 'connected' && state.phase !== 'failed') {
  state = await tcp.waitForChange(state.cursor)
}
// 用户点击断开时
await tcp.stop()
```

## 方法

| 方法 | 行为 |
| --- | --- |
| `configure(options)` | 按补丁更新配置。拨号接受 `host`、`port`（1–65535）、`servername`、`reconnect`。监听只接受 `port`（0–65535）。省略的字段保留原值，`host: null` 和 `servername: null` 清除对应覆盖值。整体验证通过才应用，错误或相同的配置保留原连接。 |
| `start()` | 有端口时启动一次，已启动时幂等。若上次已失败，可显式再试。缺少端口会返回错误。调用返回表示插件接受启动请求，建连或绑定结果通过状态读取。 |
| `stop()` | 取消当前尝试和重试计时，释放拨号 socket，或关闭监听器及其全部入站连接。清理结束后返回。重复调用安全，当前运行时配置仍可读取。 |
| `status()` | 立即返回最新快照。 |
| `waitForChange(cursor)` | 游标与当前快照一致时等待下一次变化，其他情况立即返回最新快照。可用支持取消的调用上下文结束等待。实例重载会产生新游标，调用方重新获取服务后可传入旧游标。 |

### 状态字段

`role` 为 `dial` 或 `listen`。`desired` 为 `running` 或 `stopped`。`config` 给出端口、显式主机和校验名、重试开关，凭据仅由部署提供。`cursor` 是不透明的实例版本，适合断点续读最新状态。多次变化可以合并成一个快照。

| `phase` | 含义 |
| --- | --- |
| `idle`、`awaiting-config`、`stopped` | 分别为手动模式尚未启动、自动模式等待端口、已显式停止。 |
| `dialing`、`handshaking`、`connected` | 正在拨号、正在 TLS 握手、已通过本端 TLS 校验并交给 `sessions.open`。业务可用性以相应服务的注册或调用结果为准。 |
| `starting`、`listening` | 正在启动监听或已绑定。监听时 `boundPort` 给出系统实际端口，`connections` 统计通过准入并交给 bridge 的连接。 |
| `retrying`、`failed` | 正在退避，或尝试已结束。恢复条件取决于重试策略。 |

实际地址可用时以 `localAddress`、`remoteAddress` 返回。`lastError` 保留最近的传输错误码，例如 `ECONNREFUSED`、`certificate-rejected`、`EADDRINUSE`。`retryCount` 记录本轮已安排的重试次数，`retryAt` 在退避期间给出下一次尝试的 UTC 时间。

## 自动连接和改址

- 托管实例默认 `autoStart: true`，拨号默认 `reconnect: true`。缺少端口时先提供控制服务，补齐即启动。`autoStart: false` 等待显式启动。静态行省略 `controlService`，仍须给出端口，加载即启动，拨号默认单次。
- 运行中修改主机、端口或 TLS 校验名会结束旧连接与退避，立即按新目标尝试一次，`reconnect: false` 同样如此。监听实例改端口会释放原监听及其所有客户端。新端口占用时可读到 `failed` 和 `EADDRINUSE`，再改端口或调用 `start()` 即可重试。
- 只修改重试开关会保留健康连接。退避期间关闭重试会取消下一次尝试，临时故障后重新开启会恢复拨号。临时故障从 0.5 秒指数退避到 30 秒，稳定连接 60 秒后重置。证书拒绝或本地凭据无效每 30 秒再试，已知 TLS 不兼容则停止自动尝试。重新连接会创建新会话，旧调用不会重放。
- `stop()` 后改地址只保存当前实例的配置，下次 `start()` 使用新目标。运行时修改仅保存在内存，重载或宿主重启时从部署配置初始化。
- 拨号支持 DNS、IPv4、IPv6。证书校验优先使用显式 `servername`，其次使用显式 `host`。只给端口时仍拨 `127.0.0.1` 并校验 `localhost`。监听固定于 IPv4 回环，需要转发时可使用现有的反向隧道。[传输设计](06-link-tcp.md)
