# 两个只允许出站的宿主通过 gRPC 相连

两个宿主都挂载 `go:link-grpc`，向目录登记，再主动拨到同一个区域中继。宿主无需开放入站端口。中继配对两条 HTTP/2 双向流，按原顺序转发每条消息。两端在获准后调用 `rutis/sessions.open`，通过 rutis 的 `Hello` 建立会话。已有入站 gRPC 服务也可以复用自己的端口，让设备直连内嵌插件。[直连接入](link-plugins.md#设备插件连接-go-云服务内的插件) · [云链接协议](../../link/grpc/wire/cloud.proto)

## 运行示例

示例包含一个目录、east 和 west 两个区域、edge 和 worker 两个宿主。edge 的 `wire:go:worker-jobs` 行让 worker 执行已安装的 Go 插件。edge 调用 `device.submit` 后，云端通过同一会话回调进度。

1. 从仓库根目录构建下面的程序，并把两个 `link-grpc` 分别放进两端的 Go 插件目录。
2. 示例使用同一台机器的回环地址和受信 CA。分别部署时，在目录配置中填写两端都能到达的中继地址，在中继、宿主配置中填写目录地址。将三个监听进程放在各自配置的地址。
3. 按示例配置提供 PEM 文件。目录和两台中继的服务端证书分别带 `directory.test`、`east.test`、`west.test` DNS SAN 和 serverAuth。区域代理使用单独的 `east-agent.test`、`west-agent.test` 客户端证书。宿主和运维分别使用 `edge.test`、`worker.test`、`operator.test` 客户端证书，均带 clientAuth。给目录进程一个可写的持久化目录存放 `stateFile`。配置路径见[可运行的清单](../../link/grpc/example/relay)。

```sh
cd link/grpc
go build -o /tmp/grpc-cloud ./cmd/grpc-cloud
go build -o example/relay/edge/plugins/go/link-grpc ./cmd/link-grpc
go build -o example/relay/worker/plugins/go/link-grpc ./cmd/link-grpc
go build -o example/relay/edge/plugins/go/device ./example/device
go build -o example/relay/worker/plugins/go/cloud-service ./example/cloud
```

分别在终端运行以下五个长驻进程。首次启动目录后有 12 秒的等待期，让前一进程曾签发的区域租约到期。

```sh
/tmp/grpc-cloud example/relay/control.json
/tmp/grpc-cloud example/relay/east.json
/tmp/grpc-cloud example/relay/west.json
rutis-host run example/relay/worker/rutis.json
rutis-host run example/relay/edge/rutis.json
```

在 edge 上运行 `rutis-host call --config example/relay/edge/rutis.json device.submit '"build"'`。首次返回 `{"id":"job-1","progress":"job-1 queued"}`。要在本机用临时证书复现完整部署，从仓库根目录运行 `npm ci --prefix link/tcp/node` 和 `make integration-grpc`。该套件也会模拟区域与目录之间的网络分区。[部署测试](../../link/grpc/deployment_test.go) · [真实宿主切换测试](../../link/grpc/relay_hosts_test.go)

整组停机时，先停止两个中继，等 edge 撤下远端服务，再停止宿主和目录。此顺序保留宿主清单。在 rutis-host 0.23.0 上，直接对仍有活动远端业务行的宿主发 SIGINT，偶尔会在等待行卸载时达到 10 秒退出期限。如果只想退役某个远端业务行，可在连接可用时调用 `rutis/loader.remove` 并等待返回。该操作会修改可编辑清单。

## 集成方配置

宿主行使用 JSON，任何语言的宿主或挂载方都可提供。顶层 `address`、`serverName` 和 `caFile` 指向目录。设置 `cloud.pair` 和 `cloud.target` 选择配对及对侧成员。另一端使用同一个 pair，target 填本端成员名。目录从凭据得出租户和成员。配对名可在不同租户中重用。[edge 配置](../../link/grpc/example/relay/edge/rutis.json) · [worker 配置](../../link/grpc/example/relay/worker/rutis.json)

| 设置 | 行为 |
| --- | --- |
| `certFile`、`keyFile` | 双方验证 TLS。目录按已验证叶证书的精确 DNS SAN 查找成员。 |
| `cloud.bearerFile` | 文件包含一个 API key，或一个 OAuth 签发的 RS256 JWT。仅使用 bearer 时只需要服务端 CA。每次登记、拨号都会重读令牌。 |
| `cloud.region` | 可选的首选区域。省略时，首次拨号并行探测允许且健康的区域，以首个成功的 TLS/gRPC 往返作为首选。探测总时限 1.5 秒，失败后使用允许列表的顺序。 |
| `cloud.capabilities`、`cloud.load` | 可选的能力名称数组和 0–100 负载值。连接期间每 10 秒向目录刷新。Go 嵌入方可调用 `Client.UpdateRegistration(capabilities, load)` 更新下一次报告。 |

Go 代码可使用 `MountClient` 和返回的 `Start`、`Stop`、`Status`。状态包含阶段、所选区域、generation、最近 gRPC 状态码、结构化原因和下次重试时间。`admitted` 表示传输获准，业务就绪可以通过一次服务调用确认。协议不兼容时停止自动重试，更新程序后重新启动。授权失败按 30 秒重试，普通断线按 0.5–30 秒退避。[客户端实现](../../link/grpc/client.go)

## 目录、准入和观测

- 每个配对配置一个租户、两个互为目标的成员，以及 1–16 个允许承载其数据的区域。`maxPairs[tenant]` 限制同时占用的配对，包含等待对端的预留。空闲预留最迟在最后一次签发或活动报告后的 42 秒到期，区域租约到期也会释放。区域的 `maxStreams` 包括等待和已配对的流，两端各占一个名额。
- 使用 API key 时，把高熵随机 key 的 SHA-256 十六进制摘要填入成员的 `keySHA256`。JWT 模式用成员的 `subject` 加上目录的 `jwt` 对象，其中 `issuer`、`audience`、`scope` 和 `publicKeyFile` 均必填。目录验证 RS256 签名、有效期、签发方、受众、主体和 scope。API key 和 JWT 原文只发给目录。配置变更通过重启相应服务生效。 原生客户端刷新登记时如遇拒绝，会关闭该流。需要强制终止已签发凭据对应的所有活动流时，可排空相应中继。
- 目录签发 30 秒有效的一次性票据，限定区域实例、配对、目标和 generation。登记时出示证书的端点还须出示同一张叶证书。中继以自己的 mTLS 身份向目录核销票据。收到两端的流才确认准入，单边等待 20 秒会收到 `Unavailable` 和 `PAIR_TIMEOUT`。该错误的 `google.rpc.ErrorInfo` 含 pair、target、generation。
- 运维用 `adminCert` 指定的证书调用 `rutis.link.v1.Cloud/Inspect`。返回成员的登记、最近上报时间和新鲜度，以及各区域的租约状态、配对、流数、转发消息数、payload 字节数和错误码计数。流量计数随中继进程重启清零。下例在 `link/grpc` 下执行。

```sh
grpcurl -cacert /etc/rutis/grpc/ca.pem -cert /etc/rutis/grpc/operator.pem \
  -key /etc/rutis/grpc/operator.key -authority directory.test \
  -import-path . -proto wire/cloud.proto -d '{}' 127.0.0.1:7443 rutis.link.v1.Cloud/Inspect
```

## 切换与部署边界

配对保留在当前健康区域。区域代理每 4 秒续租，12 秒内无法续约即自行终止等待中和已连接的流，即使客户端到中继的 socket 仍然可用。目录可为下一代配对选择另一允许区域。所有允许区域都离线时返回 `Unavailable`。单个客户端到中继的局部路由故障，在该区域持续续租期间会重试原区域。

新流重新完成 rutis 握手，旧会话的引用、回调和会话挂载的插件实例随旧会话释放。已经发出的 RPC 可能在对方产生了副作用，调用者会收到失败，传输不会重放。业务需要重试写操作时，可用自己的请求 ID 去重。宿主进程内独立于会话的服务仍按自身生命周期运行。

目录使用一个活动实例和一个持久化的 `stateFile`，保存单调递增的 generation。每个区域只允许一个持有租约的中继实例。Kubernetes 部署应将每个区域地址指向该实例，给目录挂载独占持久卷。终止 TLS、转发原始帧的中继属于可信边界，能够读取 payload。目录只处理登记、路由和计数。数据驻留列表约束中继选址，控制面元数据由部署方另行选址。

两跳均须支持原生 HTTP/2 双向流、trailers 和至少 16 MiB 加 protobuf 开销的消息。中继每个方向最多处理一条在转发的消息，写入等待传播背压。客户端有活动流且连接空闲时按 5 分钟 keepalive、20 秒超时探测。代理的流时长、缓冲、空闲超时及连接排空应与长连接匹配。此协议用于常驻宿主和服务网格，浏览器可用 WebRTC。[gRPC 流控](https://grpc.io/docs/guides/flow-control/) · [gRPC keepalive](https://grpc.io/docs/guides/keepalive/)
