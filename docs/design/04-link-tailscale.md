# Go 版 Tailscale 连接插件

[English](04-link-tailscale.en.md)

`go:link-tailscale` 把两个已加入同一 tailnet 的 rutis 0.23.0 宿主连接起来。系统模式复用已登录的 `tailscaled`。需要独立节点身份时，可以给同一行配置 `tsnet`。连接由 Tailscale 的访问策略和监听行的 `allowPeer` 共同控制。远端 rutis ID 由会话握手决定。[Tailscale 访问策略](https://tailscale.com/docs/features/access-control) · [tsnet](https://tailscale.com/kb/1244/tsnet)

## 系统模式

在仓库根目录用 Go 1.27.4 或更高版本编译。把同一个二进制放到两个宿主的 `plugins/go` 目录，并在两端 `rutis.json` 中配置 `"runtimes": {"go": {"dir": "plugins/go"}}`。

```sh
mkdir -p plugins/go
go -C link/tailscale build -o ../../plugins/go/link-tailscale ./cmd/link-tailscale
```

假设 tailnet 节点名分别是 `alpha` 和 `bravo`，访问策略允许 alpha 连接 bravo 的 TCP 7443 端口。双方的宿主进程需要可读取本机 Tailscale LocalAPI。bravo 的系统模式需要操作系统可以绑定本机 tailnet IP。先把监听行加入 bravo 的 `rows`。

```json
{"id":"from-alpha","name":"go:link-tailscale","config":{"listen":":7443","allowPeer":"alpha"}}
```

再把拨号行加入 alpha 的 `rows`。

```json
{"id":"to-bravo","name":"go:link-tailscale","config":{"dial":"bravo:7443"}}
```

`dial` 和 `allowPeer` 接受节点的 tailnet IP、MagicDNS 短名或完整域名。拨号只选本机守护进程网络图里的节点地址。`:7443` 只绑定本节点的首个 tailnet IP。若要指定 IPv6，可写 `[本机 tailnet IPv6]:7443`。端口 `0` 会在日志中输出实际地址。监听端对每条连接执行 TCP WhoIs，校验源 IP 属于该节点，再校验 `allowPeer`。同一获准节点重新连接时，监听端结束前一条传输后再挂新会话，不需要解析 rutis 的 Hello。

## 独立节点

在拨号或监听行的 `config` 中加入以下字段。给每行配置唯一的节点名和私有持久化绝对目录。首次注册可通过插件进程的 `TS_AUTHKEY` 环境变量注入授权密钥。已注册节点重启时沿用目录里的身份。卸载该行会关闭它自己的 tsnet 节点。系统模式下，卸载只释放插件的连接与监听。

```json
{"tsnet":{"hostname":"rutis-bravo","stateDir":"/var/lib/rutis/tailscale/bravo"}}
```

对端的 `dial` 或 `allowPeer` 使用这个独立节点在 tailnet 中的实际名称或 IP。拨号端和监听端可以分别选择系统模式或 tsnet。

## 通过管理插件启停

管理插件声明并注入 `rutis/loader`，复用上面的行配置。以下代码在管理插件里创建并启动拨号行。保留返回的行 ID，作为后续管理操作的参数。

```ts
const loader = ctx.use('rutis/loader')
const id = await loader.create({
  id: 'to-bravo', name: 'go:link-tailscale', config: { dial: 'bravo:7443' }
})
```

调用 `loader.status()` 查看行的加载状态，`loader.reload(id)` 重启，`loader.remove(id)` 停止并移除该行。再次启动时用保存的配置调用 `create`。对于随宿主启动的静态行，也可以直接编辑 `rutis.json` 并重载。绑定失败会使监听行加载失败。拨号行在后台等待对端，连接结束后从 0.5 秒开始指数退避，最长 30 秒；稳定连接一分钟后重置退避。行处于运行状态时可能正在重试。验证业务可用性时，调用一次远端服务。

## 诊断与验证

| 现象 | 检查项 |
| --- | --- |
| `tailscaled-unavailable` / `tailnet-not-ready` | 检查本机守护进程、登录状态和 LocalAPI 权限。 |
| `peer-not-found` / `node-key-expired` | 核对节点名、网络策略可见性和节点密钥有效期。 |
| `tailnet-dial` / 超时 | 核对目标端口、监听进程、设备在线状态和访问策略。ACL 丢包也可能表现为超时。 |
| `peer-not-allowed` / `unknown-peer` | 检查监听行允许的节点与实际连接来源。 |
| `path=direct` / `DERP` / `peer-relay` | 日志记录传输挂载时观测到的路径，尚无路径信息时为 `unknown`。中继的时延和吞吐可能变化，系统节点可用 `tailscale status` 查看当前路径。 |

适配器复用 Go TCP bridge 的 4 字节大端长度前缀和 16 MiB 单帧上限。会话断开后重新建立，旧消息不会重放。rutis 0.23 的 Go 接口无法读取握手后声明的 rutis ID 或配置会话发送队列上限。[TCP bridge](../../link/tcp/bridge.go) · [Tailscale CLI](https://tailscale.com/docs/reference/tailscale-cli)

在仓库根目录运行以下命令。集成测试需要 PATH 中的 rutis-host 0.23.0，也可通过 `RUTIS_HOST` 指定二进制。它启动本地测试控制面、DERP 和两个独立节点，验证远端调用、移除、重建、对端准入和自动重连。另设 `RUTIS_TAILSCALE_SYSTEM_TEST=1` 可在已登录的本机节点上用两个宿主进程验证系统模式。

```sh
go -C link/tailscale test -race ./...
go -C link/tailscale test -tags rutishost -run TestHosts ./...
```
