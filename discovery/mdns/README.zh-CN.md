# 用 Go 和 DNS-SD/mDNS 在局域网发现服务

[English](README.md)

`go:discover-mdns` 可以在指定网卡上浏览服务实例，也可以公布本机已有的入口。浏览行提供具名的只读 rutis 服务，组网策略可据此选择邻居、把拨号目标交给 link 插件。准入和会话建立由 link 负责。[组网设计](../../docs/research/network-groups.md)

## 公布的对象

**一条公告描述一个 DNS-SD 服务实例，也就是其他节点在这条局域网上可以尝试连接的一个入口。** 例如，“名叫 `office` 的实例提供 `_rutis._tcp` 服务，目标是 `edge-b.local.` 的 TCP 端口 `7443`。”服务类型描述客户端连上后应使用的协议，实例名让客户端区分同一类型的多个入口。直接监听和透明 TCP 转发都可以提供这个入口。只要对外提供相同协议和地址，两种部署就可以发布相同内容。

```mermaid
flowchart LR
    config["发布行配置的端口（现有）"] --> ad["服务实例 office<br/>_rutis._tcp · edge-b.local.:7443"]
    ad -->|"在指定网卡上公布"| peer["局域网中的浏览者<br/>解析地址和端口"]
```

| 浏览者想知道什么 | 示例中的 DNS 记录 |
| --- | --- |
| 哪些实例提供这个服务？ | PTR 将 `_rutis._tcp.local.` 指向 `office._rutis._tcp.local.`。 |
| 选中这个实例后连到哪里？ | SRV 给出 `edge-b.local.` 和端口 `7443`。A、AAAA 给出该主机的 IPv4、IPv6 地址。 |

现有发布行配置服务类型、实例名、端口和发布网卡。系统 Bonjour 或 Avahi 提供本机的目标主机名及地址。DNS-SD 还支持用 TXT 记录附带服务属性，当前插件传入的属性集为空。浏览者在本机补上接收网卡、浏览行 ID 和根据记录 TTL 算出的过期时间，再交付候选快照。对端身份由建连时的准入流程验证。[DNS-SD 实例解析](https://www.rfc-editor.org/rfc/rfc6763.html#section-5)

跨链接器的公开连接说明采用带类型和版本的协议参数。SSH 的两个地址、libp2p 的中继路径和 WebRTC 的信令信息各有自己的结构，见[通用连接说明设计提案](../../docs/design/connection-descriptions.md)。当前的入口发布行随加载登记、随卸载撤回。

## 在同一局域网运行两行配置

在仓库根目录用 Go 1.26 构建，两端使用 rutis-host 0.23.0。把二进制放进两端配置的 Go 运行时目录。

```sh
mkdir -p plugins/go
(cd discovery/mdns && go build -o ../../plugins/go/discover-mdns ./cmd/discover-mdns)
```

A 的 `rutis.json` 如下。将 `eth0` 换成 A 的局域网网卡。网卡须已启用，网络须允许 UDP 5353 组播。`domain` 默认为 `local.`。

```json
{
  "id": "node-a",
  "runtimes": {
    "go": {
      "dir": "plugins/go"
    }
  },
  "rows": [
    {
      "id": "browse-lan",
      "name": "go:discover-mdns",
      "isolate": {
        "lan-discovery": "management"
      },
      "config": {
        "interface": "eth0",
        "domain": "local.",
        "browse": "_rutis._tcp",
        "service": "lan-discovery"
      }
    }
  ]
}
```

B 配置相同的 Go 运行时目录，在 `rows` 中加入下面这行。将网卡换成 B 的局域网网卡，端口填该网卡上已经可达的入口端口。发布端通过系统服务公布本机地址。在两台主机上分别执行 `rutis-host run rutis.json`。

```json
{
  "id": "announce-ingress",
  "name": "go:discover-mdns",
  "config": {
    "interface": "eth0",
    "publish": {
      "type": "_rutis._tcp",
      "instance": "node-b",
      "port": 7443
    }
  }
}
```

发布支持 amd64 和 arm64 的 macOS、Linux。macOS 自带 Bonjour。Linux 需运行 Avahi 并安装 DNS-SD 兼容库，Debian/Ubuntu 的包名是 `libavahi-compat-libdnssd1`。仅发布时才加载该库。配置行等待注册成功，如果系统因重名而分配其他实例名，本行会报错。卸载行会撤回通告，系统发现服务发生故障后可重新加载行。

每行可浏览一种服务类型、发布一个实例，也可同时浏览和发布。要观察另一张网卡，给它单独配置一行及服务名。类型使用 `_name._tcp` 或 `_name._udp`。实例名是一个不超过 63 字节的 UTF-8 标签，可包含空格和点。入口所在的主机决定何时启用或卸载发布行。

## 在插件里读取候选

消费行声明 `"inject": ["lan-discovery"]` 和 `"isolate": {"lan-discovery": "management"}`，与 A 的浏览行匹配。Go 插件可导入 `github.com/arcships/rutis-network/discovery/mdns`，在 `Apply` 中读取快照。示例中的 `updateNeighbors` 由消费方按自己的选邻策略实现。

```go
var discovery struct {
    Snapshot      func(context.Context) (mdns.Snapshot, error)
    WaitForChange func(context.Context, string) (mdns.Snapshot, error)
}
if err := row.Use("lan-discovery", &discovery); err != nil {
    return err
}
row.Go(func(ctx context.Context) error {
    state, err := discovery.Snapshot(ctx)
    for err == nil {
        // 用最新候选替换消费方的本地视图。
        updateNeighbors(state.Candidates)
        state, err = discovery.WaitForChange(ctx, state.Cursor)
    }
    return err
})
return nil
```

两个方法都返回最新完整快照。空列表表示当前没有候选。传入未知游标或重启前的游标会立即得到当前快照。传入当前游标会等待变化，调用方可取消等待。已有实例刷新 TTL 也可能产生新游标。

```json
{
  "cursor": "sample/2",
  "candidates": [{
    "source": "browse-lan", "interface": "eth0", "domain": "local.",
    "serviceType": "_rutis._tcp", "instance": "node-b", "transport": "tcp",
    "addresses": ["192.0.2.8", "fe80::8%eth0"], "port": 7443,
    "expiresAt": "2026-11-27T08:02:00Z"
  }]
}
```

候选按 `source`（浏览配置行 ID）、`interface`、`domain`、`serviceType` 和 `instance` 区分。`expiresAt` 取本次快照中 PTR、SRV 和地址的最早到期时间。单个地址过期时，下一份快照可能保留该实例的其他地址。TCP/UDP 类型、端口和地址来自 DNS。交给 link 前，消费方需核对作用域、有效期及本地允许的地址范围，预期对端身份由本地策略另行提供。

## 验证

在 `discovery/mdns` 下执行。实网测试需要 `PATH` 中有 0.23.0 的 `rutis-host`，或将 `RUTIS_HOST` 设为该可执行文件路径。选择系统发现服务允许发布的网卡。macOS 可用 `lo0`，Linux 选用 Avahi 已启用的网卡。

```sh
go test -race ./...
go vet ./...
MDNS_TEST_INTERFACE=lo0 go test -race -count=1 -run TestLiveRows ./...
```

实网测试分别加载浏览行和发布行，等到候选解析成功后卸载发布行，验证候选随之消失。独立的单元测试覆盖分包解析、各记录单独过期、缓存刷新与撤回、IPv6 网卡作用域和游标等待取消。[mDNS](https://www.rfc-editor.org/rfc/rfc6762) · [DNS-SD](https://www.rfc-editor.org/rfc/rfc6763)
