# 从物联网设备到通用网络的协议栈调查

[English](network-stacks.en.md) · [网络群体与服务边界](network-groups.md)

接入一只 Thread 温控器之前，先问设备公开什么。边界路由器可以让 IPv6 数据包到达温控器。如果温控器只实现 Matter，插件还需要在获准的 fabric 内，以控制器身份读取温度。同一根网线接出的 Modbus TCP 仪表也有类似区别。**能到达一个地址、能与设备交换协议消息、能与设备建立 rutis 会话，是三项独立条件。** [Thread 边界路由器](https://openthread.io/guides/border-router) · [Matter](https://csa-iot.org/all-solutions/matter/)

本调查按**可观察的协议族和交接接口**盘点常见、行业专用、罕见和存量网络。一个部署可以跨多行，例如无线接入、IP 适配、传输、设备模型各占一层。表中列代表协议和关键变体，厂商私有扩展、新版本和跨层排列可沿同一方法补录。每行最后给出集成判断与代表性一手资料。

## 网络协议有多少

这份调查按插件面对的接口归并为 52 个条目。若改按可解析的线协议和格式计，规模已达数千。2026 年 11 月 27 日核对的三种统计口径如下。

| 统计来源 | 数量 | 一个计数单位代表什么 |
| --- | ---: | --- |
| IANA IP Protocol Numbers | 148 个编号 | IPv4 Protocol 和 IPv6 Next Header 共用 8 位编号空间。登记表把 0–147 逐项列出，其中含扩展首部和历史用途。148–252 当时待分配，253、254 供实验，255 保留。[登记表](https://www.iana.org/assignments/protocol-numbers/protocol-numbers.xhtml) |
| Wireshark 4.6.10 | 约 3,000 种 | 官方显示过滤器参考给出的解析覆盖规模。主协议、子协议和可解析格式的划分取决于工具。[Wireshark 显示过滤器参考](https://www.wireshark.org/docs/dfref/) |
| IANA Service Names and Port Numbers | 7,336 个不同服务名 | 从 CSV 的 `Service Name` 列去掉空值并按名字去重，同名的 TCP、UDP 登记只计一次。一个协议可服务多个注册名称，串行、非 IP 和私有协议也可能完全不在这张表里。[登记表 CSV](https://www.iana.org/assignments/service-names-port-numbers/service-names-port-numbers.csv) |

例如 MQTT 经 TLS/TCP 运行时，IP 首部只标出 TCP。抓包工具具备分别解析 TCP、TLS 和 MQTT 的能力，能否看到 MQTT 还取决于能否解密。三种口径互相交叠，无法相加得到协议总数。

对组网插件，现有清单里还可细分出几个方向：BFD 观察转发路径存活，IGMP/MLD 表达本链路的组播接收意愿，PIM-SM 在路由器间建立组播分发路径，RPKI-to-Router 提供可供路由器校验路由起源的数据，TACACS+ 用于网络设备管理访问。同样一句“节点在线且可信”，这些协议各自只能为其中一部分提供证据。[BFD](https://datatracker.ietf.org/doc/html/rfc5880) · [PIM-SM](https://datatracker.ietf.org/doc/html/rfc7761) · [RPKI-to-Router](https://datatracker.ietf.org/doc/html/rfc8210) · [TACACS+](https://datatracker.ietf.org/doc/html/rfc8907)

**怎样读这份清单**

- **系统提供 IP。** 复用已有 TCP、WebSocket、gRPC 等，前提是实际对端实现同一协议、具备资源并允许连接。位于同一 IP 网络也可能受休眠、NAT 或隔离策略限制。
- **两端都有可编程通道。** 按具体配置验证可靠、有序、双向、消息边界、有效载荷、回压和关闭。字节流需分帧，原始帧需另外提供缺少的保证。准入完成后才能交给 `rutis/sessions.open(far)`。
- **设备公开对象、命令或资源。** 由 Go 控制器或网关插件说设备原生协议，向 rutis 提供属性、事件或操作。此时 rutis 的对端可以位于网关，设备自身只运行原协议。
- **网络提供主题、数据报、短报文、单向广播或异时交付。** 按原承诺公开发布、接收、排队、过期或回执。链路确认、下一站接收、目标接收和业务执行分别记录。对实时性敏感的现场控制交给现场控制器。

实现基线为 rutis 0.23.0。本仓库已有 Go TCP、SSH、Cloudflare、Tailscale、gRPC 和 libp2p 插件。下列技术的接法属于研究判断，其他设备或协议的硬件支持需分别验证。[插件指南](../guide/link-plugins.md) · [链接契约](../design/01-link-plugin-contract.md)

## 1. 近场、家庭和园区物联

| 协议族或配置 | 交接点与限制 | rutis 的接法 |
| --- | --- | --- |
| IEEE 802.15.4 · 6LoWPAN · Thread | 802.15.4 提供低速无线帧，Thread 经 6LoWPAN 承载 IPv6。边界路由器转发 IP，休眠设备的可达性受轮询影响。 | 自有设备若能运行 rutis 和传输，可经边界路由器用现有 IP link。商用设备按其公开的应用协议接入。 [OpenThread](https://openthread.io/guides/border-router) |
| Matter · fabric · bridge | Matter 在 Thread、Wi-Fi 或以太网上使用 IP。BLE 常用于入网配置。fabric 是 Matter 的管理与信任范围。 | 控制器插件导出设备属性、命令和订阅。Thread 边界路由器负责 IP 可达，Matter bridge 才负责映射另一种设备模型。 [CSA Matter FAQ](https://csa-iot.org/all-solutions/matter/matter-faq/) |
| Zigbee · Green Power | 常见 Zigbee PRO 栈在 802.15.4 上使用自己的网络层、端点和应用集群。协调器建网，路由器与终端可承担不同角色。 | 连接协调器或网关，以端点、属性和组命令为接口。低功耗开关的一次事件足以成为插件输入。 [CSA Zigbee](https://csa-iot.org/all-solutions/zigbee/) |
| Bluetooth Classic · Bluetooth LE | Classic 的 RFCOMM 可提供串口式字节流。LE 广播、ATT/GATT 属性和 L2CAP 信道有不同的双向性、MTU 和确认方式。 | 普通传感器做 GATT 读写、通知或广播接收。两端自有时才评估 RFCOMM 或合适的 L2CAP 信道，验证分帧、顺序、断连与准入。 [Bluetooth Core](https://www.bluetooth.com/wp-content/uploads/Files/Specification/HTML/Core-60/out/en/index-en.html) |
| Bluetooth Mesh · BLE IPSP | Bluetooth Mesh 按模型和组地址分发消息。IPSP 是另一种在 BLE 上承载 IPv6 的配置。 | Mesh 网关导出模型状态和组操作。只有采用相应 IP 配置且支持传输的设备才走 IP link。 [Bluetooth Mesh](https://www.bluetooth.com/wp-content/uploads/Files/Specification/HTML/MshPRT_v1.1/out/en/index-en.html) · [RFC 7668](https://datatracker.ietf.org/doc/html/rfc7668) |
| Z-Wave · Z-Wave Long Range | 传统 Z-Wave 支持 mesh，Long Range 使用以网关为中心的星形网络。设备暴露命令类。 | 通过控制器导出节点和值，按实际网络类型判断可达性与中继能力。 [Z-Wave Alliance](https://z-wavealliance.org/z-wave-long-range-technology/) |
| Wi-SUN FAN · 6TiSCH · RPL | Wi-SUN FAN 面向户外 IPv6 场域网络。6TiSCH 是另一套在 802.15.4 TSCH 时隙跳频链路上承载 IPv6 的架构。RPL 用于低功耗有损网络中的路由。 | 在边界验证路由、时隙预算和端点传输。已有 IPv6 地址只证明寻址条件成立。 [Wi-SUN FAN](https://wi-sun.org/fan/) · [RFC 9030](https://datatracker.ietf.org/doc/html/rfc9030) |
| EnOcean · ANT/ANT+ · DECT ULE/NR+ · ESP-NOW | 能量采集报文、运动传感器配置、DECT 的不同无线配置和厂商点对点帧各有独立接口。 | 先确定设备公开的是事件、配置文件、自定义数据帧还是 IP。ESP-NOW 等自定义帧要自行解决应用交付、分段和流控。 [DECT ULE](https://www.dect.org/technologies/dect-ule/) · [ESP-NOW](https://docs.espressif.com/projects/esp-idf/en/stable/esp32/api-reference/network/esp_now.html) |
| NFC · HF/UHF RFID · UWB | 近场读写器可读标签或交换记录，UHF RFID 可盘点多个标签。常见 UWB 应用输出测距或定位。 | 公开读到的标识、记录或带时间的距离观测。若产品另有双向数据配置，再单独评估该通道。 [GS1 Gen2](https://ref.gs1.org/standards/gen2/) · [FiRa](https://www.firaconsortium.org/resource-hub/technical-faq) |

## 2. 广域、低功耗和最后一公里

| 协议族或配置 | 交接点与限制 | rutis 的接法 |
| --- | --- | --- |
| LoRa · LoRaWAN · SCHC | LoRa 是无线调制，LoRaWAN 定义设备、网关、网络服务器等角色。Class A 设备在上行后开启下行接收窗口，B 有调度窗口，C 能更长时间接收。载荷和空口预算随配置及地区变化。 | 在应用服务器侧消费上行事件、提交有期限的下行请求。区分入队、设备确认与业务执行。SCHC 可用于受限 IP 适配，仍需遵守原有空口条件。 [LoRa Alliance](https://lora-alliance.org/about-lorawan/) · [RFC 8724](https://datatracker.ietf.org/doc/html/rfc8724) |
| Sigfox · mioty · private sub-GHz | 窄带或超窄带遥测、分片传输和厂商私网有各自的下行配置、载荷与发送预算。 | 网关或运营商 API 导出小消息和投递状态，按具体套餐和设备核对返回通道。自有 LoRa 射频帧也要另外定义协议。 [RFC 8376](https://datatracker.ietf.org/doc/html/rfc8376) · [mioty](https://mioty-alliance.com/faq/) |
| LoRa P2P · Meshtastic | 同一类 LoRa 射频器件也能运行设备间的 mesh 消息协议。Meshtastic 通过邻居进行受限转发。 | 从节点 API 导出报文、来源和接收时间，区分本地排队、转发与目的地确认。LoRaWAN 应用服务器的下行配置无法套用到这张网。 [Meshtastic](https://meshtastic.org/docs/overview/mesh-algo/) |
| LTE-M · NB-IoT · 5G RedCap | 运营商和模组决定可用的数据服务。IP 承载、非 IP 数据投递、PSM 休眠和 eDRX 会改变插件看到的接口和下行时机。 | 有 IP、设备在线并能运行对端时复用 TCP 等 link。间歇唤醒或非 IP 服务使用队列和期限，按具体接入配置验证。 [3GPP TS 23.401](https://www.etsi.org/deliver/etsi_ts/123400_123499/123401/17.10.00_60/ts_123401v171000p.pdf) · [RFC 8376](https://datatracker.ietf.org/doc/html/rfc8376) |
| 2G/3G · LTE · 5G NR/SA · private APN/5G · NTN | 公网和专网可提供 IP，移动、漫游、CGNAT 和卫星链路改变入站可达性、时延或可用时段。SMS、USSD、Cell Broadcast 则提供不同的消息或交互服务。 | 宽带 IP 上复用现有 link。短信、短报文、广播各按其实际交付方式接入。运营商接受消息只确认该交接点。 [3GPP specifications](https://www.3gpp.org/specifications-technologies/specifications-by-series) |
| PLC · G3-PLC · PRIME · HomePlug/G.hn | 电力线既可承载低速抄表，也能由电力线网桥提供宽带以太网。G3 还定义了 PLC/RF 混合配置。 | 先确认交接点是表计对象、受限 IP 还是以太网口，再决定表计插件、限流连接或普通 IP link。 [G3 Alliance](https://g3-alliance.com/technologies/specifications-user-guidelines/) |
| FTTx/PON · xDSL · DOCSIS · fixed wireless · satellite broadband | 光纤、铜线、同轴、无线或卫星从接入侧交付以太网或 IP。卫星短报文产品另见异步投递。 | 在 CPE 提供的接口之上复用 link，把实测 MTU、RTT、上下行方向和故障切换纳入连接策略。 [ITU-T G.fast](https://www.itu.int/rec/T-REC-G.9701/en) |

## 3. 工业现场、楼宇、计量和车辆

| 协议族或配置 | 交接点与限制 | rutis 的接法 |
| --- | --- | --- |
| UART · RS-232/422/485 · Modbus RTU/ASCII/TCP | UART 或串口服务器可以提供字节，RS-485 规定电气接口，Modbus 定义功能和数据模型。Modbus TCP 端口的对端按 Modbus 解释请求。 | 自有的专用双工串口可实现分帧 link。对现成仪表用 Modbus 客户端导出读数，单位和寄存器含义来自设备配置。 [Modbus specifications](https://www.modbus.org/file/secure/modbusprotocolspecification.pdf) |
| PROFIBUS · FOUNDATION Fieldbus · HART · IO-Link · AS-i | 现场主站、点对点传感器、共享总线和过程仪表使用不同的轮询、供电与调度。WirelessHART 和 ISA100.11a 是工业无线族。 | 经主站或仪表网关读取过程值和诊断，保留采集时间、质量与周期。闭环控制继续由具备时序保证的控制器执行。 [IEC WirelessHART](https://webstore.iec.ch/en/publication/24433) · [IO-Link](https://io-link.com/technology) |
| EtherCAT · PROFINET · EtherNet/IP · POWERLINK · Sercos III · CC-Link IE | 工业以太网可承载循环过程数据、显式消息和管理流量。不同协议使用不同帧格式、时序和控制角色。TSN 提供以太网侧的时间敏感能力。 | 通过控制器提供的接口读取过程映像或导出服务。普通 IP 管理通道可单独接 link，周期时限由原系统验证。 [PI PROFINET](https://www.profibus.com/technologies/profinet) · [IEEE TSN](https://1.ieee802.org/tsn/) |
| CAN/CAN FD · CANopen · J1939 · ISO-TP · LIN · FlexRay | CAN 传输帧，CANopen 和 J1939 赋予其上层语义，ISO-TP 可分段传输。LIN 与 FlexRay 有各自的调度。汽车以太网还可在 IP 上提供 SOME/IP 服务。 | 选择信号、对象字典、诊断或服务发现接口。共享车辆总线上的报文标识与 rutis 端点身份分开管理。 [CAN in Automation](https://www.can-cia.org/can-knowledge/standardized-higher-layer-protocols) · [ISO-TP](https://docs.kernel.org/networking/iso15765-2.html) |
| KNX TP/RF/IP · BACnet MS/TP/IP/SC · DALI-2 | 楼控设备按组地址、对象属性或灯具地址工作，IP 变体也有自己的应用协议。 | 通过楼控控制器导出对象变化与命令结果。照明分组映射到哪个房间由部署或业务配置决定。 [BACnet](https://bacnet.org/about/) · [DALI Alliance](https://www.dali-alliance.org/d4i/) |
| M-Bus · wireless M-Bus/OMS · DLMS/COSEM | 表计可经有线、无线、电力线或蜂窝上报。DLMS/COSEM 提供对象和配套通信配置。 | 从采集器读对象和值，保留表计标识、时间、单位、状态。费率和结算属于业务。 [DLMS UA](https://www.dlms.com/core-specifications/) |
| DNP3 · IEC 60870-5-101/104 · IEC 61850 | 远动遥测、事件和控制接口各异。IEC 61850 包含 MMS 服务、GOOSE 事件和采样值等不同通信方式。 | 在站端通过协议客户端或工程网关接入，保留数据质量、站内时序和控制权限。保护与联锁行为由现场系统负责。 [IEC substation automation](https://syc-se.iec.ch/iec-63097-smart-energy-roadmap/substation-automation-system/) |

## 4. 通用数据网、云和覆盖网络

若设备交付的是 UDP socket，可对照[裸 UDP 与轻量报文协议](communication-protocols.md#6-裸-udp-与轻量报文协议)，从遥测、发现和媒体逐项判断需要哪些额外保证。

| 协议族或配置 | 交接点与限制 | rutis 的接法 |
| --- | --- | --- |
| Ethernet · Wi-Fi · Wi-Fi Direct/Aware/HaLow · Li-Fi | 以太网和常规 Wi-Fi 常由系统交付 IP。Wi-Fi Direct 建组，Aware 可以发现邻近服务，HaLow 用于亚 GHz 接入。光无线也可交付网络接口。 | 复用系统网卡、地址和现有传输。发现、关联与端点准入分别记录。若接口只暴露邻近发现，则先寻找数据通道。 [IEEE 802.11](https://www.ieee802.org/11/) |
| IPv4/v6 · TCP/UDP/SCTP · multicast/anycast · DNS-SD | TCP 是有序字节流，UDP 是报文，SCTP 可选流及顺序。IP 组播分发给组，任播通常选一个路由可达实例。DNS-SD/mDNS 负责发现。 | 选具有所需可靠性和顺序的双向通道并分帧。UDP 组播可用来通告，另行验证数据通道。任播地址也不能充当稳定的设备身份。 [TCP](https://datatracker.ietf.org/doc/html/rfc9293) · [DNS-SD](https://datatracker.ietf.org/doc/html/rfc6763) |
| VLAN · VRF · VXLAN/EVPN · MPLS/SR · SD-WAN · VPC/CNI | 交换、隔离、隧道、路由控制和云端网络插件向工作负载交付特定网络视图。BGP/OSPF/IS-IS 等协议维护路由。 | 使用获准接口和路由。网段、VRF、集群或租户的视图在发现结果中保留来源。网络编排只在需要时由部署方控制。 [EVPN overlays](https://datatracker.ietf.org/doc/html/rfc8365) |
| IPsec · WireGuard · Tailscale · OpenVPN · GRE · L2TP · IP-in-IP | VPN 或隧道可交付虚拟接口、受限网段或单个代理流。不同方案的加密与对端验证能力不同。 | 先确定交付的是 L2、L3 还是代理。在线端点用 IP link。tailnet 还可用网络图做标签发现。 [Tailscale plugin](../design/04-link-tailscale.md) |
| SSH · TLS tunnels · Cloudflare Tunnel · reverse proxies | 转发器可能提供两端字节流，也可能终止 TLS 或 HTTP 后发起新连接。反向隧道解决主动拨出与入站可达性。 | 按实际终止点判断谁是 rutis 对端。运营者设置路由和访问策略，链接行管理已配置的通道。 [SSH plugin](../design/03-link-ssh.md) · [Cloudflare plugin](../design/05-cloudflare-plugin-control.md) |
| HTTP · WebSocket · HTTP/2 gRPC · QUIC · HTTP/3/WebTransport | HTTP 请求有明确边界，WebSocket 和 gRPC 双向流可载消息，QUIC 双向 stream 有序可靠，QUIC DATAGRAM 和 WebTransport datagram 允许丢失。 | 选一个长期双向流并验证关闭、消息上限和回压。HTTP API 用客户端插件。面向不可靠 datagram 的应用可以直接消费报文。 [gRPC plugin](../design/07-link-grpc.md) · [QUIC datagrams](https://datatracker.ietf.org/doc/html/rfc9221) |
| WebRTC · ICE/STUN/TURN · libp2p · iroh · DHT | 会合、穿透、候选路径、中继和对端发现可以组合。WebRTC DataChannel 可配置无序或部分可靠。DHT 和 gossip 有独立于字节流的用途。 | 会话用可靠有序 DataChannel 或等价双向流，点对点/中继分别验证。对 DHT 查询和发布订阅提供目录或消息接口。 [WebRTC data channels](https://datatracker.ietf.org/doc/html/rfc8831) · [libp2p plugin](../design/09-p2p-plugins.md) |
| Tor · onion services · I2P | 覆盖网络可用服务名、代理或会合点连接两端，底层路由细节对应用不可见。 | 按代理或流端点接入，只报告本端可验证的连接状态。服务地址与应用对端的准入分别核验。 [Tor rendezvous](https://spec.torproject.org/rend-spec/index.html) |
| Unix sockets · named pipes · stdio · USB bulk/ADB/usbmuxd | 本机或直连设备可交付流、消息、字节管道或对已运行服务的端口转发。 | 两端有协议对端时复用流。独立确认双向性、关闭和本地准入。只提供设备操作的 USB 接口由驱动插件接管。 [ADB](https://developer.android.com/tools/adb) |
| 802.11s · OLSRv2 · Babel · BATMAN · MANET | 移动自组网可在链路或 IP 层维护邻居和路由。邻居可见、已有多跳路由、能到达应用端口是三个不同观察。 | 当系统已交付以太网或 IP 时复用网络接口。探测时注明从哪个节点和接口观察，拓扑变化后重新核对路径。 [OLSRv2](https://datatracker.ietf.org/doc/html/rfc7181) · [Babel](https://datatracker.ietf.org/doc/html/rfc8966) |
| MACsec · TLS/mTLS · DTLS · OSCORE · EAP/802.1X | 链路保护、端点传输保护、对象安全和接入认证在不同位置终止。反向代理可能是 TLS 的终点。 | 记录验证了哪一跳或哪个对象，把链路准入与业务方法授权分开。网络身份不自动改写 rutis Hello 中的对端 ID。 [OSCORE](https://datatracker.ietf.org/doc/html/rfc8613) · [Link contract](../design/01-link-plugin-contract.md) |

## 5. 设备模型、应用协议与消息中间件

各协议如何寻址、确认与补收，见[通信协议研究](communication-protocols.md)，其中用 MQTT 贯穿设备状态、离线恢复与请求结果。

| 协议族或配置 | 交接点与限制 | rutis 的接法 |
| --- | --- | --- |
| CoAP · Observe · Block-Wise · DTLS/OSCORE | CoAP 交换资源请求与响应。Confirmable 消息可获 ACK，Observe 订阅资源变化，分块用于资源传输。 | 导出读写、观察、状态码和时限。空 ACK 只能确认报文到达，业务结果要看响应及应用语义。 [RFC 7252](https://datatracker.ietf.org/doc/html/rfc7252) · [Observe](https://datatracker.ietf.org/doc/html/rfc7641) |
| LwM2M · OCF/IoTivity · oneM2M · WoT | LwM2M 提供设备管理对象与操作，OCF 和 oneM2M 提供资源模型，WoT Thing Description 描述可交互的能力及绑定。 | 通过设备管理或资源客户端导出属性、动作与事件。把对象实例、设备身份与 rutis 会话区分开。 [OMA LwM2M](https://www.openmobilealliance.org/specifications/lwm2m) · [WoT](https://www.w3.org/TR/wot-thing-description11/) |
| MQTT · MQTT-SN · Sparkplug · AMQP · NATS · Kafka | broker、主题和持久日志提供不同的订阅、确认、保留和重放语义。MQTT-SN 面向传感器网络，Sparkplug 定义工业数据和会话状态约定。 | 用消息插件保留主题、QoS、偏移或过期元数据。MQTT QoS 2 的协议交付保证并不等价于一次业务操作只生效一次。 [MQTT specifications](https://mqtt.org/mqtt-specification/) · [Sparkplug](https://sparkplug.eclipse.org/specification/) |
| OPC UA · DDS/RTPS · ROS 2 · Zenoh | OPC UA 提供客户端/服务端和 PubSub。DDS 用 topic、类型和 QoS 分发数据。ROS 2 可选择不同中间件，Zenoh 支持发布订阅与查询。 | 按实际配置桥接对象、方法、数据样本、QoS 和时间戳。控制闭环仍在具备确定性保证的一侧。 [OPC UA](https://reference.opcfoundation.org/) · [DDSI-RTPS](https://www.omg.org/spec/DDSI-RTPS/About-DDSI-RTPS) |
| SIP/RTP/SRTP · RTSP · SRT · WebRTC media | 媒体信令、播放控制和实时音视频传输有不同角色。媒体允许的丢帧、抖动与时钟处理不同于可靠控制流。 | 以媒体源、帧、轨道或播放控制服务接入。会话信令可另走 WebSocket 等，避免把音视频交付视作控制回执。 [RTP](https://datatracker.ietf.org/doc/html/rfc3550) |
| SMTP · XMPP · Matrix · ActivityPub · content-addressed distribution | 域间服务可按邮件地址、房间、收件箱或内容标识投递。对端可能是域服务器、缓存或副本。 | 保留域、订阅范围、内容校验和各阶段回执。业务对最终阅读、处理或版本选择给出结论。 [ActivityPub](https://www.w3.org/TR/activitypub/) · [Named Data Networking](https://named-data.net/project/archoverview/) |
| FTP/SFTP · SMB · NFS · WebDAV · object storage | 文件共享、远程文件系统和对象接口按文件名、句柄或对象键操作，可有缓存、分块和版本条件。 | 以文件、对象或同步插件接入。上传结束、原子发布新版本和对端应用已读取分别确认。 [WebDAV](https://datatracker.ietf.org/doc/html/rfc4918) · [NFSv4.1](https://datatracker.ietf.org/doc/html/rfc8881) |
| SNMP · NETCONF/YANG · RESTCONF · gNMI · PTP/NTP | 管理系统暴露设备状态、配置与变更流，时钟协议提供时间及同步质量。这些服务可以走独立管理网。 | 管理或观测插件保留数据模型和时间来源。路由器可响应管理查询，转发平面仍可能不可达。 [NETCONF](https://datatracker.ietf.org/doc/html/rfc6241) · [NTP](https://datatracker.ietf.org/doc/html/rfc5905) |

## 6. 专用、极端环境和存量系统

| 协议族或配置 | 交接点与限制 | rutis 的接法 |
| --- | --- | --- |
| I²C/I3C · SPI · 1-Wire · GPIO · MCTP/PLDM | 板内控制器看见寄存器、从设备或引脚。MCTP 可把平台管理消息映射到多种底层总线。 | 以驱动或本机代理公开设备操作与状态。某条总线能传字节，仍须确认是否有两个独立对端、通道所有权和返回路径。 [MIPI I3C](https://www.mipi.org/specifications/i3c-sensor-specification) · [MCTP](https://docs.kernel.org/networking/mctp.html) |
| InfiniBand · RoCE · Fibre Channel · iSCSI · NVMe-oF | HPC 互联可提供消息或远程内存操作，存储网络提供块命令和完成状态。有些设备另有普通 IP 接口。 | 需要 rutis 通道时优先用已提供的 IP 网络，存储或计算能力通过专用客户端导出。存储命令完成与工作流完成是不同事实。 [IP over InfiniBand](https://datatracker.ietf.org/doc/html/rfc4391) · [NVMe/TCP](https://nvmexpress.org/specification/tcp-transport-specification/) |
| V2X · C-V2X/PC5 · ITS-G5 · NMEA · rail/avionics | 近邻车联网可广播短时有效的路况信息。船上有 NMEA 0183、基于 CAN 的 NMEA 2000 和基于以太网的 OneNet。列车 TCN、航电 ARINC 429/664 有专用帧或时序。 | 经合适的接收器或认证网关读数据，保留来源、时间和新鲜度。双向管理通道须有明确的独立端点。 [NMEA standards](https://www.nmea.org/standards.html) |
| IEEE 11073 · BLE health profiles · DICOM | 个人健康设备可提供带单位和上下文的测量，影像系统则提供对象、查询或传输服务。两者都可运行在通用链路上。 | 设备或影像插件保留对象标识、单位和状态，授权和业务解释由应用管理。 [IEEE 11073](https://sagroups.ieee.org/11073/phd-wg/) · [DICOM](https://www.dicomstandard.org/current) |
| DVB/ATSC · DAB · paging · GNSS · optical beacons | 广播电视、寻呼、时间或定位广播通常给接收者单向数据。产品可以另设蜂窝或 IP 回程。 | 提供接收、定位、时钟或事件服务。只有独立回程允许确认时，才报告来自接收端的回执。 [DVB specifications](https://dvb.org/specifications/) |
| DTN/BPv7 · CCSDS · acoustic modems · AX.25/APRS | 计划接触的航天网络、间歇水声数传以及分组无线电可能提供分段存转、短报文或连接模式。具体协议决定服务类型。 | 能稳定双向连接时评估流，否则选邮箱、存转或事件。记录有效期、保管位置和最终交付证据。 [Bundle Protocol](https://datatracker.ietf.org/doc/html/rfc9171) · [CCSDS](https://ccsds.org/publications/sis/) · [AX.25](https://files.tapr.org/tech_docs/AX25/AX25.2.2.pdf) |
| SS7 · SIGTRAN/SCTP · IMS/SIP | 电信核心网区分信令、媒体和用户数据。SIGTRAN 可在 IP 上传送传统信令，IMS 使用多种接口控制多媒体会话。 | 一般应用消费运营商或专网网关公开的会话与投递事件。即使信令底层可达，仍需核对对端实际开放的服务。 [SIGTRAN M3UA](https://datatracker.ietf.org/doc/html/rfc4666) |
| PSTN/ISDN · modem/PPP · X.25 · Frame Relay · ATM · IPX/SPX · AppleTalk · SNA | 存量网络可能向上交付电路、虚电路、数据报、专用应用或已封装的 IP。旧系统可经前置机或终端服务器接入。 | 有 IP 交接复用 IP link，有专用双向字节通道可评估分帧，其余保留原协议语义并在边界转换。 [PPP](https://datatracker.ietf.org/doc/html/rfc1661) · [Multiprotocol over Frame Relay](https://datatracker.ietf.org/doc/html/rfc2427) |
| TETRA · DMR · P25 · MCPTT/MCData | 专业移动无线网可以提供话组、短数据、分组数据或调度台接口，取决于网络及终端配置。 | 以话组、调度事件或消息接入。仅当交付了可用的 IP 数据和受控对端时评估实时会话。 [ETSI TETRA](https://www.etsi.org/technologies/tetra) |
| PCIe/CXL · USB classes · Thunderbolt | 外围设备互联可能公开网卡、块设备、内存、音视频或厂商端点。接口外形无法决定软件契约。 | 按枚举后的功能选择网卡、专用驱动或受控数据通道。设备级操作与 IP 链路可以并存。 [USB class specifications](https://www.usb.org/documents) |

## 7. 沿完整栈验证插件边界

| 设备到插件的实际路径 | 谁是对端，如何验收 |
| --- | --- |
| 温控器 → 802.15.4 → Thread（6LoWPAN/IPv6）→ 边界路由器 → Matter 控制器插件 | 控制器在授权的 fabric 内读取属性。分别测试 IP 路径通但没有 Matter 操作授权，以及已授权的休眠节点。失败原因应能区分。 |
| Zigbee 门磁 → Zigbee 端点/集群 → 协调器或网关 → Go 插件 → rutis 事件 | 设备重新入网后按设备与端点映射到原门磁。验证旧地址不会把后续读数记在另一只设备上。门是否需要触发告警由业务决定。 |
| 表计 → LoRa/LoRaWAN Class A → 射频网关 → 网络服务器 → 应用服务器 → Go 消息插件 | 终端上行后才有通常的下行窗口。先让一条下行请求过期，再发送一条需要确认的有效下行，分别验证入队、MAC 层确认和设备应用回复。云端到插件的连接可以常驻，表计一侧仍按窗口工作。 |
| 仪表 → RS-485/Modbus RTU → 主站或 RTU/TCP 网关 → Go Modbus 插件 → rutis 服务 | 验证网关在线但仪表未应答，再验证读数的设备地址、寄存器映射、时间与单位。一条成功的 TCP 连接无法确认测量新鲜度。 |
| 自有边缘设备 → Wi-Fi HaLow 或蜂窝 IP → TCP/mTLS → rutis link → 云端插件 | 两个受控端点均运行协议。验证最大帧、休眠或换网后断线重连、会话释放，及传输恢复后业务任务是否已执行。 |
| 现场节点 → 间歇无线/卫星短消息 → 存储转发节点 → 云端收件插件 | 测试双方从未同时在线、重复投递和过期。按设备事件 ID 关联业务处理回执。 |

这些是建议实验，分别需要真实设备或能忠实复现对应配置的测试台。Go 插件可以放在能使用控制器 SDK、内核驱动或网关 API 的一侧。入网与配对、密钥安装、无线频段或运营商配置归部署方。业务分组、设备数据单位的解释、任务权限和业务回执归相应应用。

**新增一个栈时记录六件事。** 写出从介质到设备模型的实际组合、插件拿到的接口、协议终止点、发送与返回的保证、身份与密钥在哪一跳验证、影响可达性的时序或资源限制。最后给出一次最能反驳“已经连通”的观测，例如 IP 可达但资源不可读、网关接受但下行已过期。这样录入新厂商配置时，能判断它是否真的带来了新的网络能力。
