# Go 版 Tailscale 连接插件

[English](04-link-tailscale.en.md)

`go:link-tailscale` 让 rutis 0.23.0 宿主通过 tailnet 发现并连接其他宿主。默认复用已登录的系统 `tailscaled`，也支持独立的 tsnet 节点。远端 rutis ID 由会话 Hello 提供。

## 安装

在仓库根目录用 Go 1.27.4 或更高版本编译。把二进制放到各宿主的 `plugins/go` 目录，并在各自的 `rutis.json` 中配置 `"runtimes": {"go": {"dir": "plugins/go"}}`。

```sh
mkdir -p plugins/go
go -C link/tailscale build -o ../../plugins/go/link-tailscale ./cmd/link-tailscale
```

## 按标签自动发现

宿主使用 `tag:rutis-controller`，每台 leaf 使用 `tag:rutis-worker`。把下面的标签和授权合入已有的 tailnet 策略，允许宿主连接 leaf 的 TCP 7443。可以在 Tailscale 中给现有节点分配标签，也可以用带标签的 auth key 注册新节点。[Tailscale 标签](https://tailscale.com/docs/features/tags) · [访问授权](https://tailscale.com/docs/features/access-control/grants)

```json
{
  "tagOwners": {
    "tag:rutis-controller": ["autogroup:admin"],
    "tag:rutis-worker": ["autogroup:admin"]
  },
  "grants": [{
    "src": ["tag:rutis-controller"],
    "dst": ["tag:rutis-worker"],
    "ip": ["tcp:7443"]
  }]
}
```

在宿主的 `rutis.json` 的 `rows` 中加入发现行。

```json
{"id":"leaves","name":"go:link-tailscale","config":{"discover":{"tag":"tag:rutis-worker"}}}
```

每台 leaf 安装同一个插件，在自己的 `rows` 中加入相同的监听行。

```json
{"id":"from-host","name":"go:link-tailscale","config":{"allowTag":"tag:rutis-controller"}}
```

宿主订阅本机 Tailscale 可见节点的变化，按稳定节点 ID 为每台匹配的 leaf 建立一个 rutis 会话。系统模式下，发现行可以先于 `tailscaled` 启动，守护进程就绪后会自动发现 leaf。宿主会依次尝试同一 leaf 的可见地址，因此也可以连接只监听 IPv6 的双栈 leaf。leaf 默认监听自己首个 tailnet IP，逐连接使用 WhoIs 校验来源节点与标签。新增带标签的 leaf 会自动接入，改名会保留原连接，撤销标签或移出网络图会关闭对应连接。每 30 秒也会重新核对一次。本机 LocalAPI 无法确认节点状态时会关闭这些连接，恢复后重新发现。插件只需本机 LocalAPI，无需 tailnet 管理令牌或节点地址清单。要换端口，同时修改 `discover.port`、leaf 的 `listen` 和 tailnet 授权。

连接建立后，集成方按 rutis 服务名调用。多个 leaf 的服务处于同一 rutis 作用域时，给需要分别调用的服务使用不同名称。rutis 0.23 的 `<连接行>:<插件>` 只寻址该行的首个会话；需要通过这个语法在指定 leaf 加载插件时，使用下面的单节点连接行。

例如集成测试中，两台 leaf 分别提供 `one` 和 `two`。在宿主上执行以下命令，两次调用都会从相应的 leaf 返回 `"hello"`。

```sh
rutis-host call --config rutis.json one.echo '"hello"'
rutis-host call --config rutis.json two.echo '"hello"'
```

## 按名称或 IP 连接单个节点

假设 tailnet 节点名分别是 `alpha` 和 `bravo`，访问策略允许 alpha 连接 bravo 的 TCP 7443 端口。双方的宿主进程需要可读取本机 Tailscale LocalAPI。bravo 的系统模式需要操作系统可以绑定本机 tailnet IP。先把监听行加入 bravo 的 `rows`。

```json
{"id":"from-alpha","name":"go:link-tailscale","config":{"listen":":7443","allowPeer":"alpha"}}
```

再把拨号行加入 alpha 的 `rows`。

```json
{"id":"to-bravo","name":"go:link-tailscale","config":{"dial":"bravo:7443"}}
```

`dial` 和 `allowPeer` 接受节点的 tailnet IP、MagicDNS 短名或完整域名。拨号端从本机网络图解析目标，并在每次 TCP 拨号前后用 WhoIs 核对地址所属节点的稳定 ID。指定名称时可以回退到该节点的其他地址，指定 IP 时只拨这个地址。`:7443` 只绑定本节点的首个 tailnet IP。若要指定 IPv6，可写 `[本机 tailnet IPv6]:7443`。端口 `0` 会在日志中输出实际地址。监听端对每条连接执行 TCP WhoIs，校验源 IP 属于该节点，再校验 `allowPeer`。同一获准节点重新连接时，监听端结束前一条传输后再挂新会话，不需要解析 rutis 的 Hello。

## 独立节点

在发现、拨号或监听行的 `config` 中加入以下字段。给每行配置唯一的节点名和私有持久化绝对目录。首次注册可通过插件进程的 `TS_AUTHKEY` 环境变量注入授权密钥。已注册节点重启时沿用目录里的身份。卸载该行会关闭它自己的 tsnet 节点。系统模式下，卸载只释放插件的连接与监听。

```json
{"tsnet":{"hostname":"rutis-bravo","stateDir":"/var/lib/rutis/tailscale/bravo"}}
```

对端的 `dial` 或 `allowPeer` 使用这个独立节点在 tailnet 中的实际名称或 IP。拨号端和监听端可以分别选择系统模式或 tsnet。

## 通过管理插件启停

管理插件声明 `inject: ['rutis/loader']`。在 `apply` 返回后，从管理命令或事件处理函数中执行以下代码，动态创建并启动发现行。保存返回的行 ID，供后续重载或移除。

```ts
const loader = ctx.use('rutis/loader')
const id = await loader.create({
  id: 'leaves', name: 'go:link-tailscale', config: { discover: { tag: 'tag:rutis-worker' } }
})
```

用 `loader.status()` 查看行的加载状态。`loader.reload(id)` 重启发现行并重新连接可见的 leaf。`loader.remove(id)` 关闭该行的全部会话并移除配置行。需要再启动时，用保存的配置调用 `create`。静态行也可以直接编辑 `rutis.json` 并重载。

绑定失败会使监听行加载失败。拨号行在后台等待对端，连接结束后从 0.5 秒开始指数退避，最长 30 秒；稳定连接一分钟后重置退避。行处于运行状态时可能正在重试。验证业务可用性时，调用一次远端服务。

## 诊断与验证

| 现象 | 检查项 |
| --- | --- |
| `tailscaled-unavailable` / `tailnet-not-ready` | 检查本机守护进程、登录状态和 LocalAPI 权限。 |
| `peer-not-found` / `node-key-expired` | 核对节点名、网络策略可见性和节点密钥有效期。 |
| `tailnet-dial` / 超时 | 核对目标端口、监听进程、设备在线状态和访问策略。ACL 丢包也可能表现为超时。 |
| `peer-not-allowed` / `unknown-peer` | 检查监听行允许的节点与实际连接来源。 |
| `path=direct` / `DERP` / `peer-relay` | 日志记录传输挂载时观测到的路径，尚无路径信息时为 `unknown`。中继的时延和吞吐可能变化，系统节点可用 `tailscale status` 查看当前路径。 |

适配器复用 Go TCP bridge 的 4 字节大端长度前缀和 16 MiB 单帧上限。会话断开后重新建立，旧消息不会重放。rutis 0.23 的 Go 接口无法读取握手后声明的 rutis ID 或配置会话发送队列上限。[TCP bridge](../../link/tcp/bridge.go) · [Tailscale CLI](https://tailscale.com/docs/reference/tailscale-cli)

在仓库根目录运行以下命令。集成测试需要 PATH 中的 rutis-host 0.23.0，也可通过 `RUTIS_HOST` 指定二进制。测试使用本地控制面与 DERP，验证按名称连接和重连，以及一个管理端同时连接两台 worker、标签撤销与恢复、节点改名和移除。另设 `RUTIS_TAILSCALE_SYSTEM_TEST=1` 可在已登录的本机节点上用两个宿主进程验证系统模式。

```sh
go -C link/tailscale test -race ./...
go -C link/tailscale test -tags rutishost -run TestHosts ./...
```
