# Web ↔ Web 与 Client ↔ Web 的 WebRTC 连接

[English](08-link-webrtc.en.md) · [设备链路议题 #8](https://github.com/arcships/rutis-network/issues/8)

适用于 rutis 0.23.0、会话协议 5。Web 指运行在浏览器页面里的端点；首版 Client 指嵌入 rutis 的原生应用或 rutis-host，网络插件用 Go/Pion。一个配对连接两个端点，双方都能提供和调用服务。发起协商的一方只负责创建 DataChannel。

| 场景 | 用户操作 | 验收时观察什么 |
| --- | --- | --- |
| Web ↔ Web | 两个用户在各自的浏览器里接受同一应用的配对邀请 | A 调用 B 页面的服务，B 通过同一条连接回调 A。两台机器只需浏览器。 |
| Client ↔ Web | 用户在网页中选择已授权的桌面客户端或设备 | 网页调用设备插件，设备回调网页。重启设备业务插件后，原连接仍能完成调用。 |

## 从配对到第一次调用

1. 集成方让用户登录、选择对端或接受邀请，给两个获准互连的端点分配同一个 `pairId`。应用已有的 HTTPS/WebSocket 服务即可传递信令。能接收邀请的原生应用和浏览器都可以充当任一角色。
2. 两端通过本地信令服务取得本次配对授权。服务为同一次尝试分配一个发起方、一个应答方和一个短期 `attemptId`。发起方创建通道并发出包含 ICE 候选地址的 offer，应答方回复 answer。每端把收集到的 ICE 候选随一份 SDP 发布。浏览器取得可用中继候选后，可以在其他网卡仍在收集时发布。
3. ICE 选择可用路径，DTLS 校验与受信 SDP 绑定的证书指纹。适配器检查授权仍有效、对端及通道符合约定后，一端一条通道交给一个会话。此时可以观察传输已连通；第一次业务 RPC 成功才证明对端服务可用。rutis 自行处理 Hello、服务发现和调用。[会话交接契约](01-link-plugin-contract.md) · [WebRTC 规范](https://www.w3.org/TR/webrtc/)

## 跑起来

在构建机上使用 Go 1.26 和 Node.js 22.18 或更高版本。下面的命令在仓库根目录执行。它会生成浏览器模块 `dist/link-webrtc.mjs` 和同时提供传输、HTTP 信令两种插件的 Go 可执行文件。

```sh
cd link/webrtc
npm ci --prefix web
npm run build --prefix web
mkdir -p plugins/go
go build -o plugins/go/link-webrtc ./cmd/link-webrtc
go build -o dist/turn-example ./example/turn
export TURN_SECRET="$(openssl rand -hex 32)"
export STUN_URL="stun:127.0.0.1:3478"
export TURN_URL="turn:127.0.0.1:3478?transport=udp"
export PAIR_A_TOKEN="$(openssl rand -hex 24)"
export PAIR_B_TOKEN="$(openssl rand -hex 24)"
printf 'A: %s\nB: %s\n' "$PAIR_A_TOKEN" "$PAIR_B_TOKEN"
(
  ./dist/turn-example 3478 &
  relay_pid=$!
  trap 'kill "$relay_pid" 2>/dev/null || true' EXIT
  node example/server.mjs
)
```

这会同时启动本机的认证 TURN 和 `http://127.0.0.1:8080` 上的配对页面。在两个浏览器里分别填入不同的页面 ID（例如 `page-a`、`page-b`）和对应的 A、B 配对凭据。两端连接后，填入对方的服务名，点击 **Call echo** 即可互调。示例把页面 ID 作为服务名。跨设备使用时，将页面和 `/signal` 放在两个端点都能访问的 HTTPS 域名下。

**接入自己的 TURN。** 用自己的服务替换示例的回环服务器，启动信令前设置 `TURN_URL`（如 `turn:turn.example.org:3478?transport=udp`）和 `TURN_SECRET`。TURN 服务器需启用对应的共享密钥认证，并向两端开放监听端口及中继端口范围。使用 coturn 时，启用 `use-auth-secret`，让 `static-auth-secret` 与信令服务的 `TURN_SECRET` 相同。信令服务分别为两个成员签发 TURN 凭据，有效期默认一小时，可通过 `TURN_TTL_SECONDS` 配置。两端在有效期过半时申请续期，共享密钥仅保留在服务端。两端均选 **Require TURN**，连通后应显示 `connected · relay`。选择 `all` 时 ICE 也可直连，`STUN_URL` 可用于地址发现。[coturn 认证和端口配置](https://github.com/coturn/coturn/blob/master/examples/etc/turnserver.conf)

## 浏览器开发和打包

在 `link/webrtc` 使用上面示例的配对凭据和可选的 TURN 设置，执行 `npm run dev --prefix web`，即可启动页面和同源信令。保存浏览器 TS 会重新构建 ESM 并刷新已打开的页面。构建失败时终端显示错误，页面仍能读取上一份成功产物。保存 Node 配对模块会重启示例服务。页面刷新后重新连接即可建立新会话。

在 `link/webrtc/web` 执行 `npm pack`，即可生成包含浏览器 ESM 和类型声明的压缩包。在应用内安装该包，通过 `@arcships/rutis-link-webrtc` 导入 `createWebRtcLink`、`createPageEndpoint` 和 `httpSignaling`。普通 HTML 可以直接导入构建生成的 `dist/link-webrtc.mjs`。示例信令服务和 TURN 服务各自部署。安装 Chromium 后，执行 `npm run test:delivery` 可验证开发重载、构建出错后恢复、仓库外消费端的严格类型检查和真实 RPC。

## 原生宿主与页面集成

将下列内容保存为 `link/webrtc/rutis.json`。从同一目录执行 `web/node_modules/.bin/rutis-host run rutis.json`，让宿主进程继承成员 A 的凭据。网页使用成员 B 的凭据。业务插件与信令、传输分别挂行，插件重载后可以继续使用现有连接。远程部署时将 `url` 改为实际 HTTPS 信令地址。

```json
{
  "id": "device",
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [
    {
      "id": "signaling",
      "name": "go:link-webrtc-signaling",
      "isolate": { "app-pairing": "local" },
      "config": {
        "service": "app-pairing",
        "url": "http://127.0.0.1:8080/signal",
        "tokenEnv": "PAIR_A_TOKEN"
      }
    },
    {
      "id": "device-wire",
      "name": "go:link-webrtc",
      "inject": ["app-pairing"],
      "isolate": { "app-pairing": "local", "device-link": "local" },
      "config": {
        "pairId": "desk-and-page",
        "signalingService": "app-pairing",
        "controlService": "device-link",
        "iceTransportPolicy": "relay"
      }
    }
  ]
}
```

原生插件自带 `rutis/sessions` 注入。任意语言的管理插件均可用行字段 `"inject": ["device-link"]` 和 `"isolate": { "device-link": "local" }` 获取控制服务。信令和管理接口都使用宿主的私有作用域。静态配置可以省略 `controlService`。

页面引入构建好的模块。这里的 `memberToken` 由集成方从当前已认证的配对会话取得。集成方也可以传入自己的信令提供方。默认值为 `autoStart: true`、`reconnect: true`、`iceTransportPolicy: "all"`。

```js
import { createPageEndpoint, createWebRtcLink, httpSignaling } from './link-webrtc.mjs'

const page = createPageEndpoint('page-b')
const remove = page.provide('display', { echo: value => value })
const link = createWebRtcLink({
  sessions: page.sessions,
  signaling: httpSignaling('/signal', { headers: { Authorization: 'Bearer ' + memberToken } }),
  config: { pairId: 'desk-and-page', iceTransportPolicy: 'relay', autoStart: false }
})
await link.start()
```

双方都通过会话发布和调用服务。以原生端提供 `sensor.echo` 为例，页面可通过 `page.remoteServices()` 查看是否已发现 `sensor`，然后调用 `await page.call('sensor', 'echo', value)`。原生端也可以注入页面提供的 `display`。调用 `remove()` 只撤下该服务，`await link.stop()` 则关闭整条连接。

`createPageEndpoint(id, { onServiceError })` 通过 `{ service, operation, error }` 报告服务发布和撤下时收到的拒绝。同名冲突只影响这个服务，其他服务仍可调用。一方撤下本地同名服务后，即可发现另一方的服务。页面端点声明 services 能力族，可以调用以函数引用记录或对象引用形式发布的服务。

传入页面处理函数的回调在 `await` 后仍保留父调用路径。若还需要在异步操作之后发起另一项服务调用，可在进入处理函数时取得 `page.captureCall()`，在缺少原生异步上下文的浏览器中也能保留调用关系。

```js
page.provide('display', {
  async render(value, done) {
    const call = page.captureCall()
    await draw(value)
    await call('sensor', 'ack', value)
    return done(value)
  }
})
```

| 控制方法 | 约定 |
| --- | --- |
| `configure(patch)` | 可改 `pairId`、`iceTransportPolicy`、`reconnect`。先校验整份补丁。运行中更换配对或 ICE 策略会释放旧连接、重新授权。仅改重试开关保留健康连接。 |
| `start()` / `stop()` | 幂等。启动后等待另一端到场；停止会取消信令等待和退避，关闭通道并等待本地传输清理。从 stopped 配置新目标后需再次启动。 |
| `status()` / `waitForChange(cursor)` | 返回 `desired`、`phase`、`route`、`pairId`、`retryCount`、`cursor`，有错时返回 `lastError`，退避时附 `retryAt`。游标过期立即返回当前快照，支持管理插件重挂。 |

`phase` 从 `pairing → negotiating → connected`，另有 `stopped`、`retrying`、`failed`。`route` 根据选中的 ICE 候选对更新，任一端为 relay 时为 `relay`，两端候选均已知且均非 relay 时为 `direct`，无统计或连接结束时为 `unknown`。`all` 允许 ICE 选路，`relay` 强制使用 TURN，缺少可用 TURN 配置时报 `turn-unavailable`。状态和日志只给出路径类别和错误码。[WebRTC 连接统计](https://www.w3.org/TR/webrtc-stats/)

## 集成方提供的信令接口

这是本地服务契约，网络上传递信令的 URL、登录方式、房间和邀请 UI 由应用决定。管理方配置的“地址”是逻辑对端 `pairId`。STUN/TURN 地址和短期 TURN 凭据由提供方按尝试交给网络插件。

| 方法 | 调用与返回 |
| --- | --- |
| `acquire({pairId, after, waitMs, renewOf?})` | 返回 `{attemptId, role, expiresAt, iceServers}` 或 `null`。首次 `after` 为 null，重连传上次已结束的 ID。相同请求可重试。两个已授权成员取得同一尝试、互补角色和各自的 ICE 配置。 |
| `publish({pairId, attemptId, type, sdp})` | 发起方发布 offer，应答方发布 answer。同一消息重投幂等，同时拒绝替换已发布内容。 |
| `receive({pairId, attemptId, type, waitMs})` | 只读对端在此尝试发布的消息；返回 `{type, sdp}` 或 `null`。消息可重读至过期。等待有时限，停止后丢弃在途回复。 |

提供短期 TURN 凭据时，信令方可在两端的初始授权中附上同一个 RFC 3339 UTC 时间 `refreshAt`。届时两端重新调用 `acquire`，将最初的 `attemptId` 作为 `renewOf`，上轮协商 ID 作为 `after`。两端都请求续期后，服务返回新的 ID、原 `renewOf` 和角色、新 ICE 凭据、协商截止时间和下次 `refreshAt`。等候另一端时返回 `null`。双方以新的 ID 交换 SDP，在现有 PeerConnection 上重启 ICE，沿用同一条 DataChannel、rutis 会话及在途调用。续期时刻需要给双方收集候选和交换 SDP 留足时间。省略 `refreshAt` 的既有提供方只执行初次协商。

两端的 HTTP 客户端都接受 JSON `null` 或 HTTP 204 表示本次轮询暂无结果。ICE 服务器的 `urls` 字段可传单个 URL 字符串或字符串数组。

提供方负责认证信令来源、保护 SDP 的机密性和完整性，并把配对双方、角色、尝试与有效期绑定。房间名仅用于寻址。接收者只使用当前授权中对端的 SDP，拒绝错误角色、过期、重放和跨配对消息。房间授权意味着允许对端成为受信 rutis peer。应用在邀请被消费后才允许首次配对，依其授权策略决定后续重连。TURN 凭据、原始 SDP、候选地址和指纹只在协商所需的私有接口内流转。既有信令短暂中断时，已建立的 DataChannel 可以继续工作。[WebRTC 安全架构](https://www.rfc-editor.org/rfc/rfc8827)

`role` 取 `offerer` 或 `answerer`，`type` 取 `offer` 或 `answer`。`expiresAt` 使用 UTC 的 RFC 3339 时间，作为本次协商的最晚完成时间；过期后重连需获取新尝试。提供方按已认证的成员分配角色，同一端重复领取得到相同结果。适配器启动工作后即返回控制调用，`waitForChange` 用于等待实际连通。健康连接中的一端拒绝另一个尝试顶替它。

## 通道、消息与清理

发起方创建唯一的二进制通道，label 为 `rutis`，protocol 为 `rutis-link/1`，使用可靠、有序传输。应答方校验这些属性，拒绝额外通道。双方先安装接收与关闭处理，再挂载会话。Go 注入 `rutis/sessions`；浏览器适配器接受页面端点的 `open(far) → near`。适配器只处理不透明字节。[Go 使用的 Far/Near 形态](../../link/tcp/bridge.go) · [WebRTC Data Channels](https://www.rfc-editor.org/rfc/rfc8831)

首版的线上格式如下。一个 DataChannel 二进制消息承载一条记录。每条记录总长取 **16 KiB 与协商发送上限的较小值**为上限。每条完整 rutis 消息最多 16 MiB。两个限额分别约束一次 WebRTC 发送和一次会话交付。[SCTP 消息大小协商](https://www.rfc-editor.org/rfc/rfc8841)

| 首字节 | 剩余内容 | 行为 |
| --- | --- | --- |
| `0x01` | 4 字节无符号大端总长度，随后是第一段数据 | 先验证总长度再分配内存。总长度为零时，只有长度字段。 |
| `0x02` | 一个或多个后续数据字节 | 追加到当前消息，恰好达到总长度后，调用一次 `near.receive`。 |
| `0x03` | 空 | 接收方在 `near.receive` 成功后确认一次。发送方收到确认，才开始该方向的下一条消息。 |

首段就能装下的消息立即交付，非空消息首段至少含一个数据字节。发送方检查协商上限能容纳首段。文本、乱序或多余的记录、超限、截断均终止连接；部分消息随连接释放。每方向最多一条在途消息，确认发送独立于本方的数据发送等待；浏览器和 Go 都按发送缓冲水位暂停或恢复。这样能向发送方传递慢接收者的背压，代价是短消息流每条需等一次确认。rutis 0.23 的会话总出站队列仍由宿主管理。更大的业务值沿用应用分块与重组。[TCP 的大调用约定](06-link-tcp.md)

会话打开期间最多暂存第一条完整消息，直到拿到 `near` 才交付并确认。发送缓冲设高低两个阈值，给确认记录保留发送空间。通道关闭、中止或行卸载时，同时解除等待确认、缓冲腾空和接收完成的操作。

传输关闭时结束本地会话一次，rutis 关闭会话时关闭通道并唤醒发送者。短暂的 ICE `disconnected` 允许恢复，连续 10 秒未恢复或进入 `failed` 后释放旧连接。连接故障在开启 `reconnect` 时从 0.5 秒退避到 30 秒，并用新授权和 PeerConnection 重试。配对被拒或通道协议不兼容时进入 `failed`，由新的有效配置或显式启动恢复。页面刷新按新端点连接；同一行上的新尝试只在旧传输清理后启动。

## 浏览器端点与验收

页面端点复用固定版本 `@arcships/rutis-runtime@0.23.0` 的会话与编解码模块，构建时接入浏览器平台 API。页面通过 `provide` 显式导出异步服务，支持方法调用、回调、二进制值与服务撤下。升级 rutis 时需重新验证这些模块和协议。原生与页面都通过同一份网络记录协议交付消息。[浏览器端点](../../link/webrtc/web/src/browser/endpoint.ts) · [浏览器构建](../../link/webrtc/web/scripts/build.mjs)

```sh
cd link/webrtc
npm ci --prefix web
npm exec --prefix web -- playwright install chromium firefox
npm test --prefix web
go test -race ./...
```

端到端测试使用原版 rutis-host 0.23、Chromium、Firefox，以及回环地址上的真实 STUN/TURN 服务器。两种端点组合都运行直连和强制 TURN，检查双方实选路径与双向 RPC。中继测试终止 TURN 进程，等待远端服务撤下，再用同一端口启动 TURN，确认能再次调用。短期凭据测试让一个回调跨过原凭据到期时间，检查实选中继端口已变化，并在同一会话上完成挂起的 RPC 和新调用。测试还覆盖页面刷新、业务插件重载、回调、跨记录的二进制值、16 MiB 消息边界和背压。跨公网 NAT 拓扑与具体生产 TURN 部署需要另行验证。[端到端测试](../../link/webrtc/web/test/hosts.test.mjs) · [浏览器单元测试](../../link/webrtc/web/test/channel.test.mjs) · [Go 边界测试](../../link/webrtc/records_test.go)
