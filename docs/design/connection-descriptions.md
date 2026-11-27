# 让 mDNS 传递不同链接器的公开连接参数

[English](connection-descriptions.en.md) · [现有 Go mDNS 插件](../../discovery/mdns/README.zh-CN.md)

Go mDNS 插件现已支持发布和发现带类型、版本的公开连接说明。发布端适配器从已就绪的入口生成参数，接收端适配器读取参数并配置本机链接器。首个已实现的映射是使用双向 TLS 的 TCP 链接。[两端运行配置](../../discovery/mdns/README.zh-CN.md)

## 一份说明表达什么

例如，SSH 发布方知道对端需要先连接跳板，再从跳板访问一个目标。它可以给出以下说明。

```json
{
  "id": "office-ssh",
  "kind": "rutis.ssh",
  "version": 1,
  "params": {
    "sshAddress": "bastion.example:22",
    "targetAddress": "127.0.0.1:7443"
  }
}
```

`id` 在同一发布者与发布作用域内标识一条可独立更新、撤回的连接方式。`kind` 和 `version` 标识公开参数的协议和格式版本，不绑定 Go 包名或 rutis 行 ID。`params` 是由该协议定义的 JSON 对象。这里的 `127.0.0.1` 从跳板一侧解释。只有 SSH 适配器需要懂得这一点。一台主机可分别公布 SSH、gRPC 等多份说明。Go 传输层可用 `json.RawMessage` 保留参数原文，协议适配器再解码为自己的结构体。

```mermaid
flowchart LR
    source["发布端链接器或适配器"] -->|"就绪时给出公开连接说明"| pub["mDNS 发布插件"]
    pub -->|"DNS-SD 公告"| browse["对端 mDNS 浏览插件"]
    browse -->|"按 kind 和 version 匹配"| adapter["对端已配置的协议适配器"]
    local["本地准入策略与凭据"] --> adapter
    adapter -->|"校验参数后生成拨号目标"| dial["现有链接器"]
```

## 用 main 上的七种链接器检验

TCP v1 的公开字段映射已经实现，其余六项为对应链接器的设计映射。发布方显式选择允许公开的字段；接收方只从自己支持的字段生成目标配置。

| 说明类型 | 可公开的拨号信息 | 接收方自己提供或验证 |
| --- | --- | --- |
| [`rutis.tcp`](../../link/tcp/config.go) | 局域网可达的 `host`、`port`，可附服务端 TLS 名称提示。回环监听经转发时公布实际对外入口。 | 允许的地址范围、预期证书身份、CA 和客户端密钥对。 |
| [`rutis.grpc`](../../link/grpc/client.go) | gRPC Tunnel 的 `address` 和可选 `serverName` 提示。它可以指向云上的插件。 | 受信服务身份、CA 和客户端密钥对。 |
| [`rutis.ssh`](../../link/ssh/config.go) | `sshAddress` 和从该跳板访问的 `targetAddress`。反向隧道可在就绪后公布跳板上实际分配的目标端口。 | 允许的跳板和转发目标、用户、known_hosts、身份文件及内层 TLS 配置。 |
| [`rutis.cloudflare-access`](../../link/cloudflared/config.go) | Access 应用的公网 `hostname`，可附内层服务的 TLS 名称提示。 | 将获准的主机名映射到本地 profile，选择转发端口及 Access 凭据。Tunnel token 由部署方持有。 |
| [`rutis.tailscale`](../../link/tailscale/plugin.go) | 指定节点时给出 tailnet 地址与端口。发布者也可明确提供标签选择条件 `tag` 和端口，表示按组发现。 | 选择可信 tailnet，在该 tailnet 中核对节点与标签，应用本机 ACL 和准入策略。 |
| [`rutis.libp2p`](../../link/libp2p/config.go) | 完整的 `dial` multiaddr，保留目标 Peer ID，使用中继时保留 relay Peer ID 和 `/p2p-circuit/` 路径。 | 允许的对端身份及本机私钥。 |
| [`rutis.webrtc`](../../link/webrtc/control.go) | 应用允许公开的信令提供方标识，如公开的 HTTPS 信令地址。仅当 `pairId` 是可公开、单凭它无法获得授权的标识时才附带。 | 将信令标识映射到受信的本地 `signalingService`。配对授权、SDP、ICE 和临时 TURN 凭据由信令流程处理。[HTTP 信令实现](../../link/webrtc/http.go) |

这些例子描述的是对端可以尝试哪条路径。发布者所在的局域网限定**谁能发现这份说明**。实际拨号可以沿 SSH 跳板、tailnet、中继或公网进行。收到的身份和主机名都是候选提示。接收方在本地匹配允许的类型、版本、路径和身份，再选自己的凭据。私钥、令牌、配置文件路径和本机注入的服务名都留在各自宿主。

## 入口要求证书时

证书要求由协议适配器解释。`rutis.tcp` v1 和拟议的 `rutis.grpc` v1 对应现有双向 TLS（mTLS）链接器，识别类型和版本即可知道客户端需要出示证书。将来某种协议若支持多种认证方式，可在它自己的 `params` 中声明所需方式。mDNS 沿用同一个公告格式。[TCP 实现](../../link/tcp/adapter.go) · [gRPC 实现](../../link/grpc/client.go)

例如，一条 `rutis.grpc` v1 公告可以给出地址 `192.0.2.8:7443` 和名称提示 `serverName=office.example`。接收方先用本地策略确认它期望连接的确实是 `office.example`，再选择本机为该服务配置的 CA、客户端证书和私钥。若缺少客户端证书，可以显示“已发现，缺少客户端证书”并跳过拨号。握手时双方出示证书链并验证对方，私钥始终留在持有方。[TLS 服务身份校验](https://www.rfc-editor.org/rfc/rfc9525.html#section-6.1.1) · [TLS 证书交换](https://www.rfc-editor.org/rfc/rfc8446.html#section-4.4.2)

首次接入时，通过受信的部署配置或经认证的配对流程配置信任根、取得客户端证书。公告即使附带公开的 CA 或指纹，也只能用于匹配已有的信任记录。公告缺少认证字段时，适配器仍执行该协议和本地策略规定的证书检查。

## 怎样放进 DNS-SD

通用模式每份说明占一个 `_rutis-link._tcp.local.` 实例。同一发布接口上的实例可共享一个只读 HTTP 说明服务。SRV 指向这个真实服务的主机和端口，例如 `edge-b.local.:8765`。SSH 示例里的跳板和内层目标都在说明中。只有启用通用模式才需提供这个 HTTP 入口，已有的原生 DNS-SD 服务继续使用自身真实入口。

- TXT 的公共字段为 `txtvers=1`、`id`、`kind`、`v`（参数格式版本）、`rev` 和 `seq`。实例每次变更时提高十进制正整数 `seq`，重发沿用原序号。TXT 或对应 PTR 仍有效时，浏览端只接受相同公告的刷新或更高序号的更新。未携带 `seq` 的旧发布端仍可被发现。`rev` 是实际传递的 UTF-8 JSON 参数字节的 SHA-256 十六进制摘要。短参数直接以一项 `p=<JSON>` 随公告发布。
- v1 的内联规则是每项至多 255 字节，含每项长度字节的整份 TXT 至多 400 字节。超出就省略 `p`。浏览者从同一网卡解析出的 SRV 地址读取固定路径 `/v1/offers/{id}/{rev}`，得到同一份参数字节。因此新增链接器和较长的中继路径都使用相同的传输规则。[DNS-SD TXT 格式与大小](https://www.rfc-editor.org/rfc/rfc6763.html#section-6.2)
- 发布网卡同时具有 IPv4 和 IPv6 地址时，HTTP 在两个地址族使用同一个 SRV 端口。显式指定 `offers.listenIP` 会绑定该地址并在 TXT 中附上 `ip`。浏览端只从同时满足地址提示、网卡 DNS 解析结果和本机 HTTP 地址策略的地址取回参数。每个获准地址分得一部分剩余超时时间。只要版本、SRV 入口和实际提供数据的地址仍有效，新增其他地址时可继续使用已校验的参数。
- 取回参数前先匹配本地支持的类型和版本。`id` 由字母、数字、连字符或下划线组成，在同一 HTTP 入口内唯一。即使省略 `p` 仍放不下公共字段时拒绝发布。外取请求绑定观察到的接口与经本地策略允许的地址，禁用重定向，限制响应大小且不携带凭据。内联和外取都校验摘要。说明的有效期取 PTR、SRV、TXT 及所用地址记录的最早到期时间。收到新 TXT 版本、记录到期或实例撤回后，丢弃迟到的旧结果。摘要用来关联版本，对端身份仍由链接器验证。

## 本地生命周期

发布行配置网卡以及要订阅的本地说明来源。每个来源可以是链接器、适配器，或拥有该入口的业务插件。来源用 `snapshot()` 和 `waitForChange(cursor)` 给出自己希望公布的完整说明集合。来源负责用协议专属的公开字段构造 `params`，包括服务就绪后的实际目标。

发布插件按来源和 `id` 对照快照。新增就登记，参数改变就更新 TXT 和对应的参数版本，条目移除或来源卸载就撤回。格式错误或重复的条目只抑制对应 ID。整份快照的外层结构无效或超出 128 条时，发布端保留上一份已接受的集合并重试。明确返回空数组会撤回全部。发布行重启后使用新的 DNS 实例后缀，先读完整快照；整机掉线由对端按 DNS 记录 TTL 淘汰说明。浏览者按观察网卡、来源及 DNS 实例区分同名记录，并把 `kind/version/params` 交给支持该版本的本地适配器。新增一种链接方式只增加两端的参数映射，mDNS 插件沿用同一套逻辑。

## 已核对的传输行为

Go 测试用一个自定义类型验证 TXT 与 HTTP 之间的切换、更新、到期、摘要校验和撤回。端到端测试在 macOS 回环网卡启动两个 rutis host，公布实际分配的 TCP 端口，通过发现建立 mTLS 会话，再通过会话挂载并调用远端插件。缺少客户端证书时握手失败。卸载说明来源后，接收端停止专属拨号器。[传输测试](../../discovery/mdns/offer_test.go) · [双 host 测试](../../discovery/mdns/test/hosts.test.mjs)
