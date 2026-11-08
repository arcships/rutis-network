# 跨网络 link 方案全景、场景与优先级：设计稿

状态：草案。日期：2026-11-08。基线：rutis 0.21.0。
关联 issue：[#2](https://github.com/arcships/rutis-network/issues/2)。依赖：[#1](https://github.com/arcships/rutis-network/issues/1)。

## 1. 目的

回答“先做哪个、为什么”，而不是列协议清单。判断依据是用户场景：插件放在哪、宿主在不在 NAT 后、用户能不能装客户端、是个人还是团队/厂商。

## 2. 候选分类

| 类别 | 例子 | 给什么 | 接 link plugin 的形状 |
| --- | --- | --- | --- |
| 直连流 | TCP、Unix socket、pipe、WebSocket、QUIC、WebTransport | 字节流或消息流 | adapter + bridge |
| 覆盖网络 | Tailscale/Headscale、ZeroTier、Nebula、NetBird、WireGuard、EasyTier、OpenZiti、Twingate、Netmaker、Innernet、Firezone、Yggdrasil、n2n、tinc、Cloudflare WARP | IP/TCP | adapter 连 TCP；准入由 overlay |
| 反向隧道 | Cloudflare Tunnel、ngrok、frp、rathole、SSH `-R`、bore、chisel、sish、zrok、Pangolin、Azure Relay、Tailscale Funnel | 本地/公网 TCP | adapter 连 TCP |
| P2P | iroh、libp2p、WebRTC、OpenZiti SDK、Veilid、Hyperswarm | QUIC 流 / data channel | 库 + framing |
| 云链接 | gRPC 控制面/relay、云运行时、K8s/service mesh、分布式云 | bidi 流 | `link/grpc` |
| 移动设备 | ADB `forward`/`reverse`、usbmuxd/`iproxy` | 本地 TCP | `link/tcp` |
| 远控 | RDP、VNC、SPICE、Citrix、PCoIP、TeamViewer、AnyDesk、RustDesk、Sunshine/Moonlight | 屏幕/输入/音频 | **应用服务，不是 link** |
| 消息总线 | MQTT、NATS、Redis、Kafka、RabbitMQ、PubNub、Ably、AWS IoT、Azure Service Bus、Google Pub/Sub | 消息/主题 | 请求/回复 + session 映射，不是连接语义 |
| 本地/物理 | pipe、Unix socket、fd、蓝牙、串口、USB、NFC、LoRa | 字节/消息 | OS API + framing |

## 3. 场景表

| 场景 | 用户怎么用 | 首选 | 摩擦 |
| --- | --- | --- | --- |
| 个人 GPU / 远程机器 | 笔记本宿主用 GPU 机器上的 Python 插件 | SSH / Tailscale | SSH 本来就有，但隧道/重连要手工；Tailscale 要账号和客户端 |
| 手机 / 设备 App | 设备插件提供相机、传感器、通知；宿主在笔记本/服务器 | Cloudflare Tunnel / Tailscale / iroh | Cloudflare 有公开入口；Tailscale 占 VPN；iroh 要嵌入 |
| Home lab / 自建 | NAS、Pi、旧 PC，各跑一个插件 | Tailscale / Headscale / frp | Tailscale 最省事；Headscale 自建；frp 要 VPS |
| 团队 / 企业 | VPC/办公室的 rutis，SSO/ACL/审计，不开公网端口 | Tailscale / Cloudflare Access / NetBird / OpenZiti | 采购、合规、供应商 |
| Demo / 教程 | 手机五分钟连上笔记本 | Cloudflare quick tunnel / ngrok / SSH | 临时 URL、公开暴露、限流 |
| 中国 / VPS | CGNAT 家庭网络，一台公网 VPS | frp / rathole / SSH `-R` | 要 VPS、TLS、运维；没有 mesh |
| 部署平台 | rutis 在 K8s/Docker/Fly.io/Railway，本地或另一个服务连进来 | 平台网络集成 | 平台特定；端口临时 |
| 分布式云 | 宿主连云控制面/relay/运行时 | gRPC | HTTP/2 代理、长流、会话粘性 |
| 远控 | 设备的屏幕/输入/音频 | link + 远控 plugin | RDP/VNC 是应用协议，不是 link |
| IoT / 只能出站 | 设备只能出站，云调它的插件 | MQTT / NATS / AWS IoT / Azure Relay | 不是连接语义；请求/回复和 session 映射 |
| 浏览器 | 网页用本地设备的插件 | WebRTC / WebTransport / WebSocket | signaling、TURN、权限 |
| 本地 / 离线 | 同机/同进程 | pipe / memory / Unix socket | 无 |

注：WebSocket 的核心已有 `websocket::dial` / `websocket::listen`，`rutis-host/listen` 也在用；不新写 `link/websocket`。第一波的参考 adapter 是 localhost TCP（#6），不是新协议。纯 HTTP 请求/响应不是 link。

### 3.1 两个具体步骤

**个人 GPU**：1) GPU 机器装 rutis 运行时；2) 笔记本写 `link/ssh` row，host/user/key 指向 GPU 机器；3) 写 `<link row>:embedder` 让对面跑插件；4) 笔记本业务插件 `use` 对面服务；5) SSH 断了 link 自己退避重连。

**手机/设备**：1) 设备装 rutis 运行时/应用；2) 监听侧用 `cloudflared tunnel` 或 Tailscale 暴露一个 TCP/WSS 端点；3) 设备侧 `link/cloudflared`/`link/tailscale` row 连入；4) 业务插件按服务名使用设备插件；5) 公网入口加 Access 或 token。

## 4. 优先级

#6 的 `link/tcp` bridge 是 **P0 地基**：SSH forward、Tailscale、Cloudflare access、ADB、usbmuxd、frp/rathole 最终都给 TCP。没有它，这些 adapter 会各写一遍 framing。

| 层 | 内容 | 理由 |
| --- | --- | --- |
| P0 | #1 契约、#2 本分析、#6 TCP bridge | 先把边界和共享 bridge 定下来；否则重复和耦合 |
| P1 | #3 SSH、#4 Tailscale、#5 Cloudflare Tunnel；#7 gRPC（仅云业务） | 覆盖“已有 SSH”“私有网络/NAT/ACL”“不能装客户端/公开入口”三类主场景；云业务才把 gRPC 提前 |
| P2 | #7 gRPC（非云业务）、#8 移动设备链路、#10 生态 | 取决于是否有云业务/设备场景；生态要等 T1 成型 |
| P3 | #8b 远控服务、#9 iroh、消息总线、企业零信任 | 应用层或早期技术；不阻塞 T1 |

### 4.1 T 层与 P 层的映射

issue #2/#10 用 T1/T2/T3，README 和本设计用 P0–P3；两者是同一顺序的两种刻度：

- **T1 = P1**：主场景必须做（SSH、Tailscale、Cloudflare Tunnel）。
- **T2 = P2**：有明确场景但依赖额外条件（frp/rathole、Headscale/NetBird、K8s/Fly.io；gRPC 仅在云业务时提到 P1）。
- **T3 = P3**：早期或应用层（iroh/WebRTC、MQTT/NATS、企业零信任、远控）。
- **P0** 不是 T 层：它是契约 + TCP bridge 的地基，先于 T1。

### 4.2 四个维度

- **业务**：T1 覆盖“已有 SSH”“私有网络/NAT/ACL”“不能装客户端/公开入口”三类最常见使用；长尾协议不先做。
- **宣发**：SSH/Tailscale/Cloudflare 用户已有认知，demo 和教程最容易讲清；iroh/WebRTC 是技术品牌项，后置。
- **厂商**：Tailscale/Cloudflare 是商业产品，适配不等于绑定；企业零信任（OpenZiti/Twingate 等）销售周期长，后置。
- **生态**：先有契约、TCP bridge 和模板，第三方才愿意写 adapter；先把 T1 做成可复制示例，再扩目录。

## 5. 每个候选的“给什么 / 怎么接”

- **SSH**：给字节流（stdio）或本地 TCP（`-L`/`-R`）；准入是 host key + 用户 key/agent；bridge 做 framing。
- **Tailscale**：给 tailnet TCP；准入是 tailnet ACL/设备身份；bridge 做 framing。不要写成“让 WebSocket 跑在 Tailscale 上”。
- **Cloudflare Tunnel**：给本地 TCP（`access tcp`）或公网 WSS；准入是 Access 服务令牌/身份；公开入口要写清风险。
- **frp/rathole/ngrok/SSH -R**：给本地或公网 TCP；准入是 tunnel token/TLS/SSH；bridge 不变。
- **gRPC**：给 bidi 消息流；准入是 mTLS/OAuth/JWT/API key；是云链接，不是设备链接。
- **ADB/usbmuxd**：给本地 TCP；准入是设备授权/配对；bridge 不变。
- **WebRTC/iroh/libp2p**：给 data channel/QUIC 流；准入是 DTLS/endpoint key/app token；先原型，不承诺生产。
- **远控协议**：给屏幕/输入/音频，是应用服务；link 只负责把通道带过去。
- **消息总线**：给消息/主题，不是连接语义；要做请求/回复和 session 映射，不放进 link 层。

## 6. 对已有 issue 的校验

- #3 SSH 的 forward 模式应显式依赖 #6；已更新。
- #4 Tailscale 的 framing 落在 #6；已更新依赖。
- #5 Cloudflare access 模式落在 #6；已更新。
- #8 的 ADB/usbmuxd/WebRTC 落在 #6；远控服务拆为 8b；已更新。
- #7 的优先级改为“云业务 P1，否则 P2”；已更新。
- #10 与 #1 的模板/一致性测试重叠：模板、目录、CI 归 #10；已更新。

## 7. 不做什么

- 不把 RDP/VNC/SPICE 做成通用 link。
- 不把消息总线当连接语义。
- 不把 gRPC 当 NAT 穿透/设备互联。
- 不把 Tailscale/Cloudflare 变成核心依赖。
- 不为“协议最多”排优先级；不承诺 iroh 生产可用。

## 8. 0.21 的现实约束

- 非 Rust 的 listen 顶替做不了（`open` 不暴露远端 id）；需要顶替的监听侧先用 Rust。
- 0.21 错误文本不稳定，文档和测试按 code。
- gRPC 默认 4 MiB 上限与 rutis 16 MiB 不一致，需要调大或分片。
- 公开入口（Cloudflare public、ngrok）必须写清安全边界。

## 9. 开放问题

- O1：Headscale/NetBird 的自建控制面是否值得单独 adapter，还是只写兼容性。
- O2：中国场景下 Tailscale 的 DERP 可用性；是否默认推荐 frp/rathole。
- O3：WebRTC 的 signaling 复用哪个 link（SSH/Tailscale/Cloudflare/gRPC）作为默认。

## 关联

- issue [#2](https://github.com/arcships/rutis-network/issues/2)
- [契约设计稿](01-link-plugin-contract.md)
- [TCP bridge 设计稿](06-link-tcp.md)
- [SKILL.md](../../.agents/skills/rutis-link-plugin/SKILL.md)
