# RCP/1 线协议

[English](wire.md)

每条握手消息或记录占用一条有序的 WebSocket 二进制消息。中继可见路由票据、长度、时序、协议版本、临时公钥和加密记录。两端从本地调用方取得角色、私钥、固定的对端公钥、授权绑定值和截止时间。拨号前由调用方授权，rutis session 的身份由后续 Hello 中的 endpoint 给出。

## 配对

采用 `Noise_KK_25519_AESGCM_SHA256`，client 为发起方。双方预先知道各自和对端的静态公钥，每次连接分别生成新的 X25519 临时密钥。Noise prologue 为四字节 `52 43 50 01` 加凭证的 binding 字节。[Noise 规范](https://noiseprotocol.org/noise.html)

1. client 发送四字节版本前缀及第一条 Noise 握手消息。
2. daemon 验证后返回相同前缀及第二条 Noise 握手消息。每条握手的加密载荷包含两个 32 位无符号大端整数，分别是发送方的单条消息上限（1 至 2³⁰ 字节）和心跳间隔（100 至 300000 ms）。带前缀的整条消息为 60 字节。
3. 消息上限取两端较小值，心跳间隔取较大值。Noise Split 依次产生 client→daemon、daemon→client 两把 AES-256-GCM 密钥。client 先发送加密 READY，daemon 验证后返回自己的 READY。两端在验证对端确认及凭证有效期后挂载 rutis session。单独重放第一条握手消息无法建立 session。
4. Go 参考实现取 10 s 和凭证剩余有效期中较短者作为握手时限。拒绝时先发送五字节二进制消息再关闭连接，`52 43 50 45 01` 表示版本不符，`52 43 50 45 02` 表示凭证失败。中继可原样转发准入前的错误码。只有加密 READY 可确认准入成功。每条连接执行一次配对。

## 加密记录

握手后使用 Split 密钥承载 RCP 消息记录。明文头为 `u32be(12) || 0x00000000 || u64be(counter)`，共 16 字节，末 12 字节是 GCM nonce。完整头作为关联数据。加密前的内容为一个类型字节及载荷，GCM 附加 16 字节认证标签。记录总长等于载荷长度加 33。两个方向的计数器各自从 READY 的 0 开始，只接受预期的下一序号，认证成功后交付并递增。新连接重新派生密钥。

| 类型 | 内容 |
| --- | --- |
| 0 | READY，仅配对时使用 |
| 1 | 一条完整 rutis 消息，允许空消息 |
| 2 / 3 | PING / PONG |
| 4 / 5 / 6 | 以 done / replaced / rejected 原因结束 session |

控制记录的载荷为零字节。标签错误、重放或跳号、未知类型、运行期收到 READY、帧格式错误都会结束连接。读取端执行协商的单条上限。收到加密 PING 时回 PONG，在 socket 上等待读取时连续三个心跳周期未收到记录则关闭。数据保留消息边界，大值分块归 session 管理。

## 错误分类

| 条件 | 原因 |
| --- | --- |
| 凭证无效、HTTP 401/403、对端公钥验证失败 | `credential-rejected` |
| 凭证过期或 HTTP 410 | `credential-expired` |
| HTTP 404 或等待对端配对超时 | `peer-offline` |
| HTTP 429/502/503/504 | `relay-unavailable` |
| 版本不匹配 | `version-mismatch` |
| 记录格式或认证错误、长度超限 | `invalid-frame`、`frame-too-large` |
| 传输中断、收到已认证的对端关闭记录 | `transport-error`、`peer-closed`、`replaced` 或 `refused` |

## 共享向量

[wire-v1.json](testdata/wire-v1.json) 中各字段均为十六进制字节串。输入包括双方静态和临时私钥、binding、两端的握手载荷及应用明文。期望输出包括两条带版本前缀的握手记录、双方确认、双向数据和关闭记录。固定私钥仅为公开测试样本。`go test -run '^TestWireVector$' -update-rcp-vector` 重新生成文件，`go test -run '^TestWireVector$'` 验证结果，客户端方向计数器为 1 时，`clientDataInvalidTag` 应得到 `invalid-frame`。接受 `clientData` 后再重放，也应得到相同错误。向量同时提供两个准入前的拒绝报文。
