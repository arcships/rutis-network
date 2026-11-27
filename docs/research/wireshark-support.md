# 从 Wireshark 协议清单看 rutis-network 应支持什么

[English](wireshark-support.en.md) · [逐项清单（CSV）](wireshark-protocols.csv) · [网络栈调查](network-stacks.md)

**最值得先补的是局域网服务发现。** 现在两端插件已经可以通过 TCP、SSH、gRPC 等建立会话，libp2p 也已经支持直接 QUIC 和中继。Tailscale 已能按标签在 tailnet 中发现节点。普通局域网还缺一个可独立使用的发现接口，让调用方找到可拨的插件，并知道记录什么时候失效。另一类缺口是原生设备和消息网络的接入。UDP 设备交付报文，MQTT broker 交付主题消息，普通设备自身可以完全不运行 rutis。[Tailscale 标签发现](../design/04-link-tailscale.md) · [组网与服务边界](network-groups.md) · [libp2p 已实现的路径](../design/09-p2p-plugins.md)

## 建议验证的六项能力

这里的优先顺序是研究判断。表中写的是 Go 插件可以独立验收的最小能力。每一项都以 Wireshark 索引中实际出现的键定位，具体能力可以跨多层协议。

| 顺序与索引键 | 插件对外提供什么 | 如何判断值得保留 |
| --- | --- | --- |
| **先做 · `dns`** | 用 DNS-SD 发布、浏览和撤回候选端点。局域网用 mDNS，在记录中保留网络接口、实例、地址、端口和有效期。业务决定选谁，已有 link 在拨号时准入。 | 两台主机可发现并拨号。同名实例出现在两个接口时能区分，撤回或 TTL 到期能移除。未获准对端即使发布了记录也无法创建会话。[DNS-SD](https://www.rfc-editor.org/rfc/rfc6763.html) · [mDNS](https://www.rfc-editor.org/rfc/rfc6762.html) |
| **先做 · `udp`** | 为只讲 UDP 或组播的设备提供数据报端点。Go 插件按配置绑定地址和接口，每次接收交付一个完整报文及来源，并按配置限制报文长度、发送速率和接收队列。 | 两个发送者可区分。用带序号的测试载荷核对丢失、重复和乱序。截断报文不会冒充完整消息，队列满有丢弃计数。序列号如由设备定义，交由设备适配器解释。[UDP 使用指南](https://www.rfc-editor.org/rfc/rfc8085.html) |
| **先做 · `mqtt`** | 在已有 broker 上发布和订阅。协议插件负责连接、订阅恢复、QoS 和 retained 标志。业务插件定义主题、载荷、设备身份和命令回执。 | 用两个普通订阅验证扇出。重新订阅时保留 broker 回送的 retained 标志，用设备时间戳判断读数新鲜度。验证 QoS 1 重投和独立的设备执行回执。[MQTT 5](https://docs.oasis-open.org/mqtt/mqtt/v5.0/os/mqtt-v5.0-os.html) · [设备上报示例](communication-protocols.md) |
| **有受限设备时 · `coap`** | 向指定资源发请求。对端支持 Observe 时可订阅变化，结果保留响应码、资源标识和新鲜度信息。 | 验证重复请求、超时、观察取消和设备离线。空 ACK 不能完成一次需要资源结果的操作。[CoAP](https://www.rfc-editor.org/rfc/rfc7252.html) · [Observe](https://www.rfc-editor.org/rfc/rfc7641.html) |
| **有 Android 对端时 · `adb`** | 沿用移动设备设计，通过已授权设备的 ADB 端口转发接已有 TCP bridge。设备侧必须有运行中的对端插件。 | 两台 rutis 宿主可完成远程调用。未授权设备无法接入，拔线能结束会话，停止对应配置能释放转发规则。[移动设备设计 PR #18](https://github.com/arcships/rutis-network/pull/18) · [ADB](https://developer.android.com/tools/adb) |
| **有浏览器对等连接需求时 · `rtcdc`** | 以 WebRTC DataChannel 搬运 rutis 消息，限定可靠、有序的双向通道。复用 ICE、STUN、TURN，由部署方提供信令和可验证的对端绑定。 | 浏览器与 Go 端在直连和强制中继下均可互调。验证准入失败、最大消息、慢读回压和关闭。部分可靠或无序的配置须在交接前拒绝。[WebRTC Data Channels](https://www.rfc-editor.org/rfc/rfc8831.html) · [链接契约](../design/01-link-plugin-contract.md) |

**组网中各角色交付的东西不同。** DNS-SD 给出带期限的候选地址。路由器选择 IP 下一跳。透明 relay 帮两端转发，准入和两端会话身份仍由相应层验证。MQTT 或 Zigbee 网关则能终止原协议，向 rutis 交付一条主题消息或设备事件。组成员资格、租户归属、温度单位和命令是否执行由业务端决定。云端插件可以嵌在云服务里，和设备侧插件共同构成 gRPC 会话的两端。[gRPC 云服务插件](../design/07-link-grpc.md) · [安全、稳定与可信](network-assurance.md)

## 清单的范围与判定方式

底稿是 2026-11-27 取得、页首标为 **4.6.10** 的 Wireshark Display Filter Reference 字母索引。官网概述解析规模约为 3,000 种协议。这里实际能逐项核对的是索引中 **2,166 个唯一过滤器键**，其中 **2,056** 个的版本范围包含 4.6.10，**110** 个只见于早期版本。这个范围也包括子协议、设备集群、抓包格式和分析器伪协议。例如，Zigbee 的不同属性集群各有过滤器，`_ws.expert` 则代表分析器信息。其他版本、未列在索引中的协议及第三方插件均在这次计数之外。[官方索引](https://www.wireshark.org/docs/dfref/) · [4.6.10 解析器源码](https://github.com/wireshark/wireshark/tree/v4.6.10/epan/dissectors)

CSV 中每个键只有一个主分类和一个结论，保留上游名称、全部版本区间和原索引里的详情页相对路径。`family` 是本调查给出的 **50 个协议族或相邻用途分组**。跨层时按主要交接点归档，例如 ADB 在 USB 与设备通道组，Matter 的蓝牙入网过滤器在 Matter 组。历史条目保留原分组，以 `historical` 覆盖当前接入结论。同一族的共性见下表，具体例外见 CSV。例子包括用于性能监控的 `pcp` 和用于 NAT 端口控制的 `portcontrol`。[逐项清单](wireshark-protocols.csv) · [Performance Co-Pilot](https://www.wireshark.org/docs/dfref/p/pcp.html) · [Port Control Protocol](https://www.wireshark.org/docs/dfref/p/portcontrol.html)

| CSV 结论 | 条目数 | 对 rutis-network 的含义 |
| --- | ---: | --- |
| `existing-link` | 5 | TCP、SSH、gRPC 有本仓库插件；QUIC 有 libp2p 路径；WebSocket 由 rutis 宿主提供。Cloudflare、Tailscale 等产品可复用多种协议，插件数和过滤器数分别统计。[链接契约](../design/01-link-plugin-contract.md) |
| `trial-link` | 2 | ADB 和 WebRTC 的受限配置值得在有相应对端时验证会话契约。 |
| `trial-service` | 4 | DNS 服务发现、UDP 报文、MQTT 主题和 CoAP 资源可以分别提供通用接口。 |
| `reuse` | 779 | 依赖系统、库、守护进程或已有网络服务提供的能力，例如 TLS、BGP、STUN。 |
| `gateway` | 406 | 有明确设备、控制器或 broker 时按需接入。一个网关往往覆盖许多过滤器。 |
| `application` | 607 | 应用或行业插件拥有对外承诺，例如文件写入、数据库事务或电话状态。 |
| `reference` | 238 | 供排障、解码或存量互通参考，当前不建议单独立项。 |
| `defer` | 15 | 11 项容迟或空间传输条目需要断续路径场景，另有 4 个当前键缺少明确插件交接点。 |
| `historical` | 110 | 上游版本范围止于 4.6.10 之前。过滤器的变化不等于底层协议已经停用。 |

## 按协议族查阅

“现行 / 全部”统计该分组中版本范围包含 4.6.10 的键数及历史在内的总键数。代表键可直接用于查 CSV。

### 连接、发现与转发

| 协议族（CSV family） | 现行 / 全部 | 代表键 | rutis 接在哪里 |
| --- | ---: | --- | --- |
| 传输与可靠组播 · `transport` | 28 / 28 | `tcp`, `quic`, `udp`, `sctp`, `pgm` | TCP 和 QUIC 已有会话路径。UDP 适合保留报文边界。SCTP 或可靠组播要有明确对端与交付需求。 |
| Web、代理与流式 RPC · `web` | 17 / 17 | `http2`, `grpc`, `websocket`, `http3` | 已有 gRPC 双向流和宿主 WebSocket。单次 HTTP 调用导出为应用方法。 |
| WebRTC 数据通道 · `webrtc` | 1 / 1 | `rtcdc` | 浏览器到插件时验证可靠有序通道。信令、ICE 和准入各有所有者。 |
| 远程终端、桌面与代理 · `remote` | 26 / 26 | `ssh`, `rdp`, `vnc`, `socks` | SSH 已能搬运会话。桌面帧、剪贴板和输入属于远控服务。 |
| 命名、地址配置与发现 · `discovery` | 24 / 25 | `dns`, `dhcp`, `srvloc` | DNS-SD 值得先做。DHCP 由系统运行，厂商发现结果只在声明的作用域内有效。 |
| NAT 穿越与中继 · `traversal` | 5 / 6 | `stun`, `turnchannel`, `portcontrol` | 在具体 link 内调用穿越库或 TURN 服务，记录实际走了直连还是中继。 |
| 隧道、VPN 与覆盖网络 · `overlay` | 34 / 34 | `wg`, `esp`, `vxlan`, `geneve` | 使用现有接口、守护进程或 SDK 提供的可达性。部署方拥有路由和凭据。 |
| IP、路由、组播与路径存活 · `routing` | 86 / 90 | `bgp`, `ospf`, `pim`, `igmp`, `bfd` | 系统路由器选下一跳。应用可观察路径或加入组播，但成员授权仍须独立判断。 |
| 认证、密钥与传输保护 · `auth` | 60 / 62 | `tls`, `dtls`, `radius`, `oscore` | 由适配器调用成熟库并实施准入。证书、网络账户和 rutis Hello 身份分别验证。 |

### 网络接入与设备接口

| 协议族（CSV family） | 现行 / 全部 | 代表键 | rutis 接在哪里 |
| --- | ---: | --- | --- |
| 以太网、交换与 VLAN · `lan` | 74 / 75 | `eth`, `vlan`, `lldp`, `stp` | 复用网卡和交换机，LLDP 可提供邻接线索。 |
| 无线 LAN 与 mesh · `wifi` | 50 / 53 | `wlan`, `wifi_p2p`, `batadv` | 先用操作系统建立网络，再按地址拨号。无线 mesh 的邻居表属于底层。 |
| 宽带与运营商接入 · `access` | 123 / 142 | `docsis`, `pppoe`, `atm`, `homeplug` | 调制解调器或接入设备交付接口。业务插件通常只看到 IP 或设备 API。 |
| 蜂窝、移动核心网与电信信令 · `cellular` | 186 / 205 | `nas-5gs`, `ngap`, `gtp`, `gsm_map` | 使用终端数据连接。需要短信或网络事件时接运营商接口，保留其交付范围。 |
| InfiniBand、RDMA 与计算互连 · `fabric` | 14 / 16 | `infiniband`, `irdma`, `ipoib` | 有 IP 接口时复用现有 link。原生队列对需要另行证明收益与资源回收。 |
| 低速无线与 IPv6 适配 · `pan` | 3 / 3 | `wpan`, `6lowpan`, `wisun` | 边界路由器可交付 IPv6。低功耗和休眠约束由上层传输一起验证。 |
| Thread 组网 · `thread` | 9 / 9 | `mle`, `thread_meshcop`, `trel` | 复用既有 Thread 网络与边界路由器，在设备实际提供的端口上接入。 |
| Zigbee、RF4CE 与设备集群 · `zigbee` | 83 / 88 | `zbee_nwk`, `zbee_aps`, `zbee_zcl` | 经协调器读取端点、属性与事件。多个 cluster 可以共用一个网关。 |
| Matter fabric 与入网 · `matter` | 3 / 3 | `matter`, `btp-matter` | 使用获得授权的控制器导出属性、命令和订阅，保留 fabric 范围。 |
| LoRaWAN 广域低功耗网络 · `lpwan` | 1 / 1 | `lorawan` | 通过网络服务器接入上行和排队下行，标记接收时间、有效期和下行结果。 |
| 蓝牙与 BLE 配置文件 · `bluetooth` | 54 / 56 | `bluetooth`, `btatt`, `btgatt`, `btrfcomm` | 复用系统蓝牙栈。按实际 profile 暴露特征值、通知或串行端点。 |
| 近场卡、读写器与 RFID · `nfc` | 8 / 8 | `iso14443`, `felica`, `llrp` | 读卡器插件公开一次读写或一条标签事件，并保留读写器与卡的身份边界。 |
| USB、设备总线与调试通道 · `usb` | 23 / 24 | `usb`, `adb`, `adb_cs` | 通用 USB 由系统驱动处理。可用获准的 ADB 转发连接设备上运行的对端插件。 |
| 打印、扫描与外围设备 · `peripheral` | 16 / 17 | `ipp`, `sane`, `i2c` | 网关交付打印作业、扫描结果或总线读写。设备回执的含义由设备定义。 |
| 设备对象、定位与厂商物联 · `iot` | 27 / 27 | `dof`, `aj`, `rtls` | 接现有控制器，公开设备原生对象和事件。按部署定义对象映射。 |

### 消息、控制与延迟交付

| 协议族（CSV family） | 现行 / 全部 | 代表键 | rutis 接在哪里 |
| --- | ---: | --- | --- |
| 发布订阅、队列与数据分发 · `messaging` | 28 / 29 | `mqtt`, `mqttsn`, `amqp`, `kafka`, `rtps` | 优先做 MQTT 边界。AMQP、Kafka 或 DDS/RTPS 则在有具体部署时按其确认、位置或 QoS 接入。 |
| 受限设备资源访问 · `resource` | 2 / 2 | `coap`, `lwm2mtlv` | CoAP 可提供通用资源接口。设备模型、单位、动作归调用方或网关。 |
| 工业控制、现场总线与机器人 · `industrial` | 82 / 85 | `modbus`, `opcua`, `cip`, `pn_io`, `tcpros` | 从控制器或端点读取寄存器、节点或事件。控制循环、命令联锁留在现场。 |
| 楼宇自动化 · `building` | 10 / 11 | `bacnet`, `kip`, `mstp` | 经 BACnet 对象或 KNX 组地址接入，项目配置决定点位。 |
| 电力、计量与远动 · `utilities` | 19 / 21 | `dnp3`, `goose`, `cosem`, `synphasor` | 网关映射测点、时标与质量。保护动作与实时组播仍由现场系统负责。 |
| 仪器与半导体设备 · `instrument` | 7 / 10 | `hislip`, `hsms`, `vxi11_core` | 连接仪器控制接口，按设备约定处理命令完成和测量数据。 |
| 车载、轨道与道路通信 · `vehicle` | 44 / 44 | `can`, `j1939`, `doip`, `someip` | 经车载网关交付信号或诊断服务，记录总线和报文来源。 |
| 容迟网络与空间数据传输 · `delay` | 11 / 11 | `bpv7`, `tcpcl`, `ltp`, `cfdp` | 出现断续路径需求时再接 bundle 队列。对外表示过期、转交和投递状态。 |

### 应用和专用行业

| 协议族（CSV family） | 现行 / 全部 | 代表键 | rutis 接在哪里 |
| --- | ---: | --- | --- |
| 网络运维、测量与时钟 · `ops` | 79 / 84 | `snmp`, `ntp`, `ptp`, `syslog`, `pcp` | 复用时钟和监控系统。需要设备管理时经管理端点获取指标或执行操作。 |
| 企业 RPC、目录与集群 · `enterprise` | 162 / 164 | `dcerpc`, `ldap`, `giop`, `thrift` | 独立的目录查询或 RPC 方法由应用插件包装，保持原系统的访问控制。 |
| 数据库、缓存与数据服务 · `database` | 16 / 16 | `pgsql`, `mysql`, `cql`, `memcache` | 应用使用驱动处理查询、事务和重试。 |
| 文件系统、块存储与存储网络 · `storage` | 73 / 75 | `nfs`, `smb2`, `iscsi`, `nvme` | 挂载、锁、写入确认和恢复由存储系统或其客户端负责。 |
| 文件交换与 P2P 分发 · `file` | 30 / 30 | `sftp`, `tftp`, `bittorrent`, `bt-dht` | 业务接口可公开文件或内容标识。协议内部的 DHT 服务其自身命名空间。 |
| 邮件、即时通信与在线状态 · `collab` | 51 / 55 | `smtp`, `imap`, `xmpp`, `msrp` | 使用账户、房间或邮箱接口。用户在线与消息已读各有业务含义。 |
| 电话信令与实时媒体会话 · `voice` | 73 / 78 | `sip`, `sdp`, `rtp`, `rtcp` | 导出通话状态或媒体质量。RTP 容许丢包，不能直接承载要求可靠交付的会话。 |
| 音视频、广播与演出控制 · `media` | 114 / 122 | `rtsp`, `srt`, `h264`, `artnet` | 播放器或控制台处理编解码、时序和灯光通道。 |
| 交易、支付与数字资产 · `finance` | 17 / 17 | `fix`, `nasdaq_itch`, `iso8583`, `bitcoin` | 序列号、行情分发和清算确认由金融应用解释。 |
| 医疗数据与通知 · `medical` | 3 / 4 | `dicom`, `hl7` | 接入影像、消息或告警系统，由业务插件处理记录标识和授权。 |
| 航空、导航与航天遥测 · `aviation` | 18 / 18 | `asterix`, `atn-cpdlc`, `gps_l1` | 在已有导航或监视系统的输出端接观察数据，保留来源和时标。 |
| 业余无线与软件无线电 · `radio` | 8 / 8 | `ax25`, `aprs`, `vrt` | 无线网关公开位置、帧或采样。呼号、信道与接收时间随数据传递。 |
| 专用无线数据交换与仿真 · `fieldradio` | 8 / 10 | `s5066dts`, `dis`, `link16` | 既有系统输出的事件可供领域插件接入，保留该系统的寻址和授权。 |
| 游戏与仿真协作 · `gaming` | 12 / 12 | `quake`, `raknet`, `mcpe` | 游戏客户端或服务器拥有实体、帧和状态同步语义。 |

### 抓包、格式与待定项

| 协议族（CSV family） | 现行 / 全部 | 代表键 | rutis 接在哪里 |
| --- | ---: | --- | --- |
| 抓包封装、元数据与分析工具 · `capture` | 137 / 142 | `frame`, `_ws.expert`, `pcapng.darwin`, `radiotap` | 用作排障和验收证据。抓包时间、接口和分析告警不会提供对端通道。 |
| 编码、文件与负载格式 · `format` | 30 / 31 | `cbor`, `json`, `protobuf`, `jpeg` | 协议适配器可使用编解码库。独立格式缺少地址、准入和生命周期。 |
| 存量网络与历史协议栈 · `legacy` | 63 / 66 | `ipx`, `ddp`, `clnp`, `x25` | 优先经已有兼容网关接入。有些过滤器在当前 Wireshark 仍可用。 |
| 交接接口待核实 · `unresolved` | 4 / 5 | `gdt`, `iap`, `interlink`, `rlm` | 缺少可验证的 rutis 对端、交接接口或部署需求，保留原键供补证。 |

## 复核与更新

从官方索引的每个协议段落取冒号前的过滤器键、链接文字、相对链接和括号内的全部版本区间，保留顺序和大小写，去掉末尾的字段数量。用过滤器键对照 CSV 时，新增、缺少、重复的键都应为零。将 `dfref_path` 接到索引地址可得到上游原链接。更新版本时应重新比较每段区间，包括不连续区间，再复审新增或重命名的键。[官方索引](https://www.wireshark.org/docs/dfref/)

为避免网页装饰变化影响复核，按 CSV 顺序将每行的 `filter_id`、`dfref_path`、`versions` 以单个制表符分隔，以换行符 LF 结尾，按 UTF-8 编码。该投影的 SHA-256 为 `b5e283cd0f1ac824633c7b5333a0a80a9db8c9e09d541ca7de9ae6bfd4f7fb25`。
