# TCP 插件的连接管理接口

任何 rutis 支持的插件语言都可以使用这套接口。TCP 拨号和监听由 Go 二进制提供。每个托管实例通过自己的具名服务提供控制对象。连接断开后仍可调用。适用于 rutis 0.23.0。[English](06-link-tcp-integration.en.md) · [构建和部署](../guide/link-plugins.md)

## 用宿主配置挂载

将 Go 二进制放入 `plugins/go`，在 `rutis.json` 中注册运行时和 TCP 行。宿主即可加载该实例。下例先等待调用方设置目标，若要在加载时拨号，可在 `config` 中给出 `host` 和 `port`。

```json
{
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [
    {
      "id": "wire",
      "name": "go:link-tcp-dial",
      "isolate": {
        "office-tcp": "management"
      },
      "config": {
        "controlService": "office-tcp",
        "ca": "/etc/link/peer-ca.pem",
        "cert": "/etc/link/client.pem",
        "key": "/etc/link/client.key"
      }
    }
  ]
}
```

## 从已有插件调用

在需要控制该实例的本地插件行中合并以下字段，保留调用方原有的语言运行时、插件入口、依赖和作用域。调用方使用自身语言的 rutis SDK 按服务名获取 `office-tcp`。仅使用远端业务服务的插件继续注入业务服务即可。

```json
{
  "inject": ["office-tcp"],
  "isolate": { "office-tcp": "management" }
}
```

例如，用户选定服务器后，将对象 `{"host":"office.example.com","port":7443}` 传给 `configure`。调用 `status()` 取得快照，再把其中的字符串 `cursor` 传给 `waitForChange`。用户断开时调用 `stop()`。方法、参数和返回值在各语言间共用。

**让控制服务和调用方处于同一个本地隔离作用域。** 上例在两行中为相同服务名指定了相同的 `isolate` 标签。宿主会将根作用域中的普通服务导出给已连接对端。[宿主导出规则](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/session/plugin/services.rs) · [集成测试](../../link/tcp/node/test/control.test.mjs)

## 从插件代码动态挂载

如果 TCP 行已由宿主配置好，管理插件直接注入其 `controlService`。如果由同一个管理插件在运行中创建 TCP 行，可使用同一 Go 二进制提供的 `go:link-tcp-attach`。它只负责在两个服务都就绪时，把 TCP 控制句柄交给管理方。

在宿主配置中声明以下隔离组。将管理插件放入该组，或给它的 `office-owner` 使用相同的隔离标签。掌握句柄即可操作连接，这两个服务都应限制在本地管理作用域。

```json
{
  "id": "local-links",
  "group": true,
  "isolate": { "office-tcp": "management", "office-owner": "management" },
  "config": [
    {
      "id": "deliver",
      "name": "go:link-tcp-attach",
      "inject": ["office-tcp", "office-owner"],
      "config": { "controlService": "office-tcp", "controlReceiver": "office-owner" }
    }
  ]
}
```

管理插件注入 `rutis/loader`，提供 `office-owner.attach(handle)`，在回调中保存本次收到的句柄。首次挂载时，在管理插件启动后的事件或服务方法中调用 `rutis/loader.create(options, "local-links")`，使用以下选项。TCP 行发布 `office-tcp`，所需的传输依赖由插件自身声明。`rutis-host run` 会把创建的行写入宿主配置。[Loader 接口](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-loader/src/loader/service.rs)

```json
{
  "id": "wire",
  "name": "go:link-tcp-dial",
  "config": {
    "controlService": "office-tcp",
    "autoStart": false,
    "ca": "/etc/link/peer-ca.pem",
    "cert": "/etc/link/client.pem",
    "key": "/etc/link/client.key"
  }
}
```

`create` 返回行 ID，收到 `attach` 表示控制句柄就绪。准备好接收回调后再发起创建，等待期间应允许回调进入。Go 的 `Attach` 可接收 `*advanced.Service`，其他语言使用各自 SDK 的远程对象代理。每条独立管理的连接使用自己的服务名和交付行。[Go 挂载和接收示例](../../link/tcp/test/fixtures/mounter/main.go)

管理插件重启时，TCP 行保留 socket、运行时配置和状态游标。管理插件重新提供 `office-owner` 后，`deliver` 会再次调用 `attach`，保存新收到的句柄即可继续管理已有行。业务插件重启期间，其他使用者仍能使用连接。卸载 TCP 行会关闭它持有的 socket，使该实例的句柄失效。重载 TCP 行会按照部署配置创建新实例。显式调用 `stop()` 则只关闭传输，控制服务仍可调用。

若使用过本地草案中的 `controlReceiver`，把它和接收服务的 `inject` 从 TCP 行移到 `deliver`，并在 TCP 行上配置 `controlService`。

## 方法

| 方法 | 行为 |
| --- | --- |
| `configure(options)` | 按补丁更新配置。拨号接受 `host`、`port`（1–65535）、`servername`、`reconnect`。监听只接受 `port`（0–65535）。省略的字段保留原值，`host: null` 和 `servername: null` 清除对应覆盖值。整体验证通过才应用，错误或相同的配置保留原连接。 |
| `start()` | 有端口时启动一次，已启动时幂等。若上次已失败，可显式再试。缺少端口会返回错误。调用返回表示插件接受启动请求，建连或绑定结果通过状态读取。 |
| `stop()` | 取消当前尝试和重试计时，释放拨号 socket，或关闭监听器及其全部入站连接。传输清理结束后返回。重复调用安全，当前运行时配置仍可读取。 |
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
- 运行中修改主机、端口或 TLS 校验名会结束旧连接与退避，随后按新目标尝试一次，`reconnect: false` 同样如此。监听实例改端口会释放原监听及其所有客户端。新端口占用时可读到 `failed` 和 `EADDRINUSE`，再改端口或调用 `start()` 即可重试。
- 只修改重试开关会保留健康连接。退避期间关闭重试会取消下一次尝试，临时故障后重新开启会恢复拨号。临时故障从 0.5 秒指数退避到 30 秒，稳定连接 60 秒后重置。证书拒绝或本地凭据无效每 30 秒再试，已知 TLS 不兼容则停止自动尝试。重新连接会创建新会话，旧调用不会重放。
- `stop()` 后改地址只保存当前实例的配置，下次 `start()` 使用新目标。运行时修改仅保存在内存，重载或宿主重启时从部署配置初始化。
- 拨号支持 DNS、IPv4、IPv6。证书校验优先使用显式 `servername`，其次使用显式 `host`。只给端口时仍拨 `127.0.0.1` 并校验 `localhost`。监听固定于 IPv4 回环，需要转发时可使用现有的反向隧道。[传输设计](06-link-tcp.md)

## 切换承载远端插件的连接

普通跨端服务调用可以直接用 `configure` 切换目标。如果宿主还通过 `wire:<plugin>` 运行远端插件，rutis 0.23.0 的 `near.end` 只确认输入结束，旧插件此时可能仍在卸载。新 socket 已连通时，原行的路由仍可能被旧会话占用。

要把这类远端插件可靠地迁到另一台宿主，先等待 `rutis/loader.remove("wire")` 返回，再调用 `rutis/loader.create(options, "local-links")`。沿用 `id: "wire"` 和原隔离组，在 `options.config` 中写入新目标和所需配置。宿主会释放旧行并将已有的 `wire:<plugin>` 行挂到新对端。新实例会产生新的控制句柄和游标，业务就绪可用一次实际服务调用确认。[原版宿主集成测试](../../link/tcp/node/test/control.test.mjs)

## 开发运行和打包加载

在 `link/tcp/node` 执行 `npm ci` 和 `npm run dev:setup`。准备命令使用 Go 和 OpenSSL 构建具有独立宿主 ID 的回环对端，生成七天有效的本地 mTLS 证书，并写入两个开发配置。首次准备时可通过 `PORT` 更改默认端口 17443。再次执行准备命令会保留已有配置和私钥，补齐缺失配置，并在证书已到期或将在一天内到期时续签。续签后重启对端和开发宿主以加载证书。

执行 `npm run dev:peer` 并保持运行。在第二个终端进入相同目录，选择要修改的插件。

| 目标 | 启动命令 | 首次连接或修改源码后的调用 |
| --- | --- | --- |
| Node/TS | `npm run dev` | `node_modules/.bin/rutis-host call --config dev witness.echo 37` |
| Go | `node_modules/.bin/rutis-host dev ..` | `node_modules/.bin/rutis-host call --config .. witness.echo 37` |

预期返回 `["loopback",37]`。修改 Go 源码后，宿主会重编译二进制。0.23 的 Node 运行时会缓存插件入口导入的模块，因此开发命令用 esbuild 监听本地 TS 模块，构建成功后原子替换单个开发入口。宿主重载该行，拨号插件重新建立连接。

在 Node 包目录执行 `npm pack`，即可生成包含 ESM 和类型声明的本地压缩包。在集成方的 Node 工程中安装该包和 `@arcships/rutis-runtime@0.23.0`，将 `runtimes.node.project` 指向该工程，行的 `name` 填 `rutis-network-tcp-reference`。该参考插件用 `port`、`ca`、`cert`、`key` 配置回环 mTLS 连接。已有源码行仍可挂载 `link/tcp/dial.ts`。迁移已有检出时请先移走旧的 `link/tcp/node_modules`，再在 `node/` 安装，以便两个入口使用同一份运行时依赖。执行 `npm run test:delivery` 可验证源码重载和仓库外安装。
