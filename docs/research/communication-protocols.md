# 消息投递、确认与恢复的通信协议研究

[English](communication-protocols.en.md) · [网络栈调查](network-stacks.md) · [网络群体与服务边界](network-groups.md)

同一台设备通过 Wi-Fi 连上 MQTT broker，可以上报温度、接收通知，也可以订阅命令。网络栈决定设备能否到达 broker。通信协议决定谁能收到哪条消息、谁来确认，以及设备离线期间消息去了哪里。对 rutis 插件而言，这些保证决定了它能向调用方报告什么。

**选型时先问四件事。** 收件人是指定对端、所有订阅者、竞争消费者中的一个，还是未来会回来的设备？发送者拿到的是协议确认、服务端持久化确认、消费者确认，还是业务结果？顺序和确认按哪条连接、会话、队列、分区或订阅生效？断线后从哪里恢复，恢复时会不会重复？

例如采集端在 10:00 离线，传感器在 10:01 至 10:03 每分钟发布一个温度。采集端在 10:04 返回时，用 Core NATS 的普通订阅只能收到重新订阅以后的发布。若在离线前已有符合条件的持久 MQTT 订阅，broker 可在会话与消息有效期内补交匹配的 QoS 1/2 消息。若只有 retained 温度，则新订阅通常只得到最后的保留值。Kafka 消费者在日志仍保留记录时，可从相应分区已提交的位置继续读。这三项分别适合在线通知、离线订阅或可重放的历史记录。[Core NATS](https://docs.nats.io/nats-concepts/core-nats) · [MQTT 5](https://docs.oasis-open.org/mqtt/mqtt/v5.0/os/mqtt-v5.0-os.html) · [Kafka design](https://kafka.apache.org/41/design/design/)

以下按设备数据、队列与日志、实时与跨域通信比较。表里同时收录开放线协议和具有实际部署语义的服务，例如 SQS API。讨论 Kafka 或 Redis 时指明所用的消息模型。CloudEvents 描述事件封装，Protobuf 和 JSON 描述数据表示，AsyncAPI 描述接口契约。投递和恢复仍需选择具体协议。[CloudEvents](https://cloudevents.io/) · [AsyncAPI](https://www.asyncapi.com/docs/reference/specification/latest)

## 1. 设备遥测、资源观察和工业数据

| 协议或服务 | 寻址与分发 | 确认与恢复 |
| --- | --- | --- |
| MQTT 3.1.1 / 5.0 | 客户端向 broker 发布到主题，普通匹配订阅各得一份。MQTT 5 另定义共享订阅，同一共享订阅内按消息选一个接收者。 | QoS 按发送方与接收方之间的一段交换生效。离线补收取决于会话、订阅、QoS 和有效期。保留消息提供主题的最近保留值。 [MQTT 3.1.1](https://docs.oasis-open.org/mqtt/mqtt/v3.1.1/os/mqtt-v3.1.1-os.html) · [MQTT 5](https://docs.oasis-open.org/mqtt/mqtt/v5.0/os/mqtt-v5.0-os.html) |
| MQTT-SN 1.2 | 传感器可通过网关发布或订阅，普通主题可注册为较短的 topic ID，另有预定义和短主题名。 | 网关负责映射主题。休眠客户端的补收受网关缓冲、唤醒和有效会话约束。保留网关与设备两侧的记录。 [MQTT-SN specification](https://mqtt.org/mqtt-specification/) |
| Sparkplug | 在 MQTT 上规定主题命名、指标载荷以及边缘节点和设备的 birth、data、death 消息。 | 桥接时保留指标定义和会话状态。birth 可重新建立指标上下文，death 表示协议报告的状态变化。停产判断还要结合业务证据。 [Eclipse Sparkplug](https://sparkplug.eclipse.org/specification/) |
| CoAP / Observe · LwM2M | 客户端按 URI 请求资源或注册观察关系。LwM2M 另定义设备对象和管理操作。 | CoAP 空 ACK 可先于最终响应抵达。Observe 适合跟踪资源当前状态，应用若需每个中间读数，应另行验证采集和历史保留。 [CoAP](https://datatracker.ietf.org/doc/html/rfc7252) · [Observe](https://datatracker.ietf.org/doc/html/rfc7641) · [LwM2M](https://www.openmobilealliance.org/specifications/lwm2m) |
| DDS / DDSI-RTPS | 发布者和订阅者在域中按 topic、类型和兼容 QoS 匹配。样本可用 key 区分实例。 | 可靠性、历史深度、durability 等 QoS 决定晚加入者可见的样本。桥接时同时保留 key、时间与样本状态。 [DDS](https://www.omg.org/spec/DDS/) · [DDSI-RTPS](https://www.omg.org/spec/DDSI-RTPS/) |
| OPC UA PubSub | 发布数据集，订阅者按配置解释元数据。可使用无 broker 的 UDP 分发或经 broker 的消息映射。 | 保留数据集版本和质量。实际传输与安全配置决定确认、重发和可恢复范围。 [OPC UA Part 14](https://reference.opcfoundation.org/Core/Part14/v105/docs/) |
| Zenoh | 使用 key expression 匹配发布、订阅和查询。可向响应查询的节点请求数据。 | 用订阅表示变化，用查询获取可用数据。历史与可查询范围取决于部署的响应方和存储。 [Zenoh](https://zenoh.io/docs/overview/what-is-zenoh/) |

## 2. 路由、任务队列和可重放的日志

| 协议或服务 | 寻址与分发 | 确认与恢复 |
| --- | --- | --- |
| AMQP 0-9-1 / RabbitMQ | 发布到 exchange，经 binding 路由到队列。同一队列的多个消费者可以分担工作。 | publisher confirm 和 consumer ACK 是两个方向的确认。需要保证路由到队列时还要处理不可路由消息。队列持久性、消息持久性和重投配置分别生效。 [AMQP 0-9-1](https://www.rabbitmq.com/amqp-0-9-1-protocol) · [RabbitMQ confirms](https://www.rabbitmq.com/docs/confirms) |
| AMQP 1.0 | 以 source/target 建立 link，transfer 携带消息，credit 控制流量。与 0-9-1 是两套线协议。 | 根据 delivery outcome 和 settlement 判断对端接受、拒绝或释放。节点是否持久化、能否恢复需看端点能力和配置。 [AMQP transport](https://docs.oasis-open.org/amqp/core/v1.0/os/amqp-core-transport-v1.0-os.html) |
| STOMP 1.2 | 客户端对 destination 发送或订阅，destination 的含义由服务端定义。 | 选择 auto、client 或 client-individual 确认。RECEIPT 针对服务端处理客户端帧，队列保留和重放查 broker 的契约。 [STOMP 1.2](https://stomp.github.io/stomp-specification-1.2.html) |
| Core NATS | 普通 subject 订阅可扇出，queue group 将匹配消息分给组内一个在线成员，reply subject 支持请求应答。 | 基础服务提供至多一次投递。订阅者离线时不会在 Core NATS 上积累其错过的消息。 [Core NATS](https://docs.nats.io/nats-concepts/core-nats) |
| NATS JetStream | stream 按 subject 捕获消息，consumer 维护投递状态。可以设置持久 consumer、起始位置和显式确认。 | 服务端的发布确认和消费者的处理确认分开。保留策略、AckWait 和重投策略决定断线恢复及重复窗口。 [JetStream](https://docs.nats.io/nats-concepts/jetstream) · [Consumers](https://docs.nats.io/nats-concepts/jetstream/consumers) |
| Redis Pub/Sub · Redis Streams | Pub/Sub 向在线匹配订阅者推送。Streams 在键下保存带 ID 的条目，可按 ID 读或用 consumer group 分工。 | Pub/Sub 断线会漏收。Streams 的 XACK 移除组的待确认记录。条目保留和 Redis 存储持久性另行配置。 [Redis Pub/Sub](https://redis.io/docs/latest/develop/pubsub/) · [Redis Streams](https://redis.io/docs/latest/develop/data-types/streams/) |
| Apache Kafka | 按 topic 和 partition 写日志，顺序边界在分区内。普通消费组协调分区消费，各组可独立读同一批记录。 | 提交 offset 记录消费位置，保留窗口内可回退重读。Kafka 事务对 Kafka 内的读写有界定范围，写外部服务需要协调副作用和位置。 [Kafka design](https://kafka.apache.org/41/design/design/) |
| Apache Pulsar | topic 可有多个具名订阅。Exclusive、Failover、Shared 和 Key_Shared 分别规定订阅内如何分配消息。 | 确认和重投以订阅为边界。backlog、已确认消息的保留和订阅模式决定能重读什么、哪些消息可能重排。 [Pulsar messaging](https://pulsar.apache.org/docs/4.0.x/concepts-messaging/) |
| Apache RocketMQ | 发布到 topic，由消费组共享处理或分别订阅。延时消息可在预定时间后变为可投递。 | 消费重试和死信策略按消费模式与组配置。记录原消息身份，防止同一事务因超时重投而重复执行。 [RocketMQ retries](https://rocketmq.apache.org/docs/featureBehavior/10consumerretrypolicy/) · [Delay messages](https://rocketmq.apache.org/docs/featureBehavior/02delaymessage/) |
| Managed queue API · Amazon SQS Standard | 通过服务 API 从队列取消息，接收后消息暂时对其他消费者不可见。 | 处理完成后用 receipt handle 删除。可见性超时后可能再投递，标准队列的调用方需要容忍重复。 [SQS visibility](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-visibility-timeout.html) |

## 3. 直连、请求应答、实时推送与跨域消息

| 协议或服务 | 寻址与分发 | 确认与恢复 |
| --- | --- | --- |
| ZeroMQ / ZMTP · libp2p GossipSub | ZeroMQ 提供 PUB/SUB、REQ/REP 等 socket 模式。GossipSub 在对等节点之间按主题扩散。 | 按实际模式暴露消息。ZeroMQ 晚加入的订阅者可能错过早期发布。P2P 网络的传播也无法确认每位预期收件人都已处理。 [ZeroMQ Guide](https://zguide.zeromq.org/docs/chapter5/) · [libp2p Pub/Sub](https://libp2p.io/docs/pubsub/) |
| HTTP webhook · SSE · Web Push | webhook 向指定 URL 回调，SSE 在一个 HTTP 响应中单向推送，Web Push 经推送服务投递给订阅端点。 | webhook 的重试由发送方约定。SSE 的 Last-Event-ID 可用于恢复，历史要由应用提供。Web Push 的 TTL 限定服务端可保留消息的时间。 [SSE](https://html.spec.whatwg.org/multipage/server-sent-events.html) · [Web Push](https://datatracker.ietf.org/doc/html/rfc8030) |
| WebSocket · Socket.IO | WebSocket 在一条连接上传送双向消息。Socket.IO 在自己的协议上提供命名事件、确认和重连。 | 原生 WebSocket 可作为受准入的会话通道。Socket.IO 的重连和回调确认遵循其自身契约，默认投递不能补齐离线历史。 [RFC 6455](https://datatracker.ietf.org/doc/html/rfc6455) · [Socket.IO delivery](https://socket.io/docs/v4/delivery-guarantees/) |
| gRPC · RSocket · WAMP | gRPC 可做单次 RPC 或流式 RPC。RSocket 支持请求响应、流和双向 channel。WAMP 路由器可提供 RPC 和 Pub/Sub。 | 保留每种协议的关联、流控、结束和错误状态。两端实现对等协议的长期可靠双向流可评估为 link。独立命名的方法则导出为应用服务。 [gRPC](https://grpc.io/docs/what-is-grpc/core-concepts/) · [RSocket](https://rsocket.io/about/protocol/) · [WAMP](https://wamp-proto.org/intro.html) |
| XMPP PubSub · Matrix | XMPP 可在节点上发布、订阅并按节点配置保存条目。Matrix 通过各自管理的 homeserver 传播房间事件。 | 保留来源域、节点或房间、发布权限与事件标识。远端服务器接受或存储事件，与用户已经读到是两个独立观察。 [XMPP PubSub](https://xmpp.org/extensions/xep-0060.html) · [Matrix federation](https://spec.matrix.org/latest/server-server-api/) |

## 4. 把 MQTT 接到 rutis

以温度采集为例。设备在 `plant/a/sensors/t1/state` 发布最近温度，在 `plant/a/sensors/t1/requests` 接受“立即采样”，然后在另一主题给出带请求 ID 的结果。两个历史温度、最后一个已知值、一次新采样的完成回执是三种不同的数据。

| MQTT 机制 | 插件应如何解释 |
| --- | --- |
| QoS 0 / 1 / 2 | 依次为至多一次、至少一次、恰好一次的协议交付等级，分别作用于发布者到 broker、broker 到订阅者的交换。订阅侧实际 QoS 还受授予的订阅等级限制。QoS 1 要容忍重复。QoS 2 也无法原子提交设备的采样和应用的数据库写入。 |
| retained、会话恢复、消息过期 | retained 用于每个精确主题的最近保留值。MQTT 3.1.1 可用稳定 Client ID 与 Clean Session=false 恢复会话。MQTT 5 区分 Clean Start 和 Session Expiry，并可设置 Message Expiry。检查 CONNACK 的 Session Present 决定是否需要重建订阅。给温度携带采集时间，识别离线后收到的旧值。 |
| 普通订阅、共享订阅、遗嘱 | 用两个独立的普通订阅可让监控端各看一份，用同一共享订阅可以让 worker 分担。Will 表示 broker 按协议触发的连接状态通知，可能受 Will Delay 影响。设备真实工况需要另外确认。 |
| 请求与结果 | MQTT 5 可用 Response Topic 和 Correlation Data。请求方先订阅响应主题，设置期限，并按请求 ID 关联。使用 3.1.1 时由两端约定响应主题与载荷。发布确认只说明发布这一步，设备应在采样完成后发自己的结果。 |
| 重投和权限 | 一次性采样命令使用非 retained 发布，并携带应用级请求 ID 和有效期。设备根据请求 ID 识别重复操作。MQTT Packet Identifier 可以复用，只对当前协议交换有意义。部署方配置主题权限。需要按调用者授权时，业务端还需可信的身份信息。普通订阅消息不会自动带上发布者的认证身份。 |

以上解释以规范的 QoS、会话、保留消息与请求响应语义为准。[MQTT 5](https://docs.oasis-open.org/mqtt/mqtt/v5.0/os/mqtt-v5.0-os.html) · [MQTT 3.1.1](https://docs.oasis-open.org/mqtt/mqtt/v3.1.1/os/mqtt-v3.1.1-os.html)

**Go 插件的职责。** 一行配置可以持有 broker 地址、凭据引用、Client ID、允许的主题过滤器和会话策略。运行时连接、订阅、限制在途消息，在卸载时停止重连并按配置释放本地资源和 broker 会话。设备事件保留主题、实际 QoS、保留标志和关联字段。载荷提供采集时间时一并保留，接收时间单独记录。业务插件通过注入发布服务或消费事件决定采样、存储与告警。双向桥应限制进出主题、标注来源并验证回送不会形成循环。Go 可分别选 Eclipse Paho 的 3.x 协议客户端或 MQTT 5 客户端。这里记录的是插件接入设计。[Paho MQTT 3.x](https://github.com/eclipse-paho/paho.mqtt.golang) · [Paho MQTT 5](https://github.com/eclipse-paho/paho.golang)

普通主题适合直接公开发布与订阅。若要让两个 rutis 插件借 broker 建立对等会话，两端还需约定对端配对与准入、每个方向的消息边界和顺序、去重、有限回压、断线关闭，以及隔离上一次连接的旧消息。满足这些条件的通道才可交给 `rutis/sessions.open(far)`。[链接插件契约](../design/01-link-plugin-contract.md)

## 5. 用故障结果验收插件

发布权撤销、保留样本的新鲜度和网关来源证明见[安全、稳定与可信研究](network-assurance.md)。

下面是研究提出的验证场景。每次都分别记录发布者、broker、订阅者和业务侧能看到的证据。

1. **订阅者离线。** 用稳定 Client ID 建立 QoS 1 订阅，断开，发布三条有序号、未过期的消息，在会话到期前回来。检查会话恢复和实际收到的序号，再用新建会话、非 retained 发布对照。
2. **晚加入。** 连续发布三个 retained 状态，新客户端普通订阅应得到最近的保留值。重新订阅命令主题时，应当没有历史命令触发采样。
3. **业务完成后确认丢失。** 在采样已写入、消费确认或应用回复尚未送达时中断。允许调用方超时或工作项重投，再用原请求 ID 查询结果。分别核对执行次数和收到的回复数。
4. **群发与分工。** 两个普通订阅者和一组有两个成员的共享订阅同时接收带唯一 ID 的测试事件。在无故障的单次投递下，两个独立订阅各收一份，共享组内一个成员接收。再中断成员并单独记录重投。
