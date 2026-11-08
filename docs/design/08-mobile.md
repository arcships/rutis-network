# 移动设备链路与远控服务：设计稿

状态：草案。日期：2026-11-08。基线：rutis 0.21.0。
关联 issue：[#8](https://github.com/arcships/rutis-network/issues/8)。依赖：[#1](https://github.com/arcships/rutis-network/issues/1)、[#6](https://github.com/arcships/rutis-network/issues/6)。

## 1. 目标

把“设备链路”和“远控能力”分开：

- **8a 设备链路**：ADB、usbmuxd、WebRTC data channel，把设备接进 rutis。
- **8b 远控服务**：screen/input/audio/clipboard 是应用服务，通过 rutis 的服务/事件使用，不塞进 link layer。

## 2. 8a 设备链路

### 2.1 ADB（Android）

```text
宿主 link/tcp ── 127.0.0.1:<local> ── adb forward tcp:<local> tcp:<device> ── 设备
```

- `adb forward tcp:<port> tcp:<port>` / `adb reverse` 给本地 TCP；bridge 用 #6。
- 准入：ADB key + 设备授权（USB 调试）；不把 ADB 身份传给 session。
- 生产限制：ADB 调试授权不适合终端用户；文档写清开发/测试边界。
- 设备端需要跑 rutis 运行时/桥；`adb reverse` 适合设备主动连宿主。

### 2.2 usbmuxd（iOS）

- `iproxy <local> <remote>` 给本地 TCP；bridge 用 #6。
- 准入：iOS 信任/配对；不把配对身份传给 session。
- 限制：需要 macOS/Xcode 环境；App 分发/调试边界写清；USB/Wi-Fi 配对差异。

### 2.3 WebRTC data channel（可选，先原型）

- data channel 作为通用消息通道；一条 channel = 一个 session。
- signaling 由已有 link 提供（SSH/Tailscale/Cloudflare/gRPC），不新造。
- 准入：DTLS 身份 + 应用层 token；不把 DTLS 指纹当 session id。
- STUN/TURN：写清 relay 依赖、隐私和成本；浏览器/移动端权限。
- 0.21 异步 `send`：data channel 的写阻塞通过 async `send` 回压。

### 2.4 listen 侧

- Rust 用 `Session::handshake` 才能同 id 顶替；非 Rust `open(far)` 不暴露 id，`session_key(row)` 先到先得，第二条同 row 的 session 不会注册 handle，所以非 Rust 监听不支持同 id 顶替，必须用 Rust（O1）。不要写“并存”。
- 非 Rust 也拿不到结构化握手错误；O1 前只能按传输错误退避，不要把 ADB/iproxy/WebRTC 断开原因当握手结果。
- 不解析 session `Hello`；不拿设备名/ADB 序列号代替 id。

## 3. 8b 远控服务

远控是应用层。建议最小服务形状（草案，不是最终 API）：

```ts
interface Screen {
  capture(format: 'png' | 'jpeg'): Promise<Uint8Array>
  stream(config: { fps: number; quality: number }): AsyncIterable<Frame>
}
interface Input {
  pointer(event: { kind: 'move' | 'down' | 'up'; x: number; y: number; button?: number }): Promise<void>
  key(event: { kind: 'down' | 'up'; code: string; modifiers?: string[] }): Promise<void>
  text(text: string): Promise<void>
}
```

- `screen` / `input` 的权限、错误、背压单独定义；不与 link 生命周期混在一起。
- 远控 plugin 可以用 RDP/VNC/SPICE/RustDesk/scrcpy/WebRTC 实现；其他插件只依赖服务。
- 音频/剪贴板后置；先 screen + input。
- 服务断开时正确卸载；session 断开时远控服务随之卸载（它是 session 下的插件）。
- 不把屏幕帧塞进 session 消息的普通请求/回复里；用流/事件，避免大队列。

## 4. 生命周期

- 设备链路断开 → `near.end`；session 卸载 → 关闭 ADB/iproxy/WebRTC 连接。
- 重连在 adapter：指数退避；设备未授权不无限重试。
- 0.20 卸载顺序：子进程/连接清理只注册一次；不在 cleanup 里注册/等待 cleanup。
- 0.21 异步 `send`：写阻塞回压；bridge 队列有界。

## 5. 错误

| 场景 | 处理 |
| --- | --- |
| ADB 未安装/设备未授权 | 报 `adb devices` 状态和授权步骤 |
| `iproxy` 不存在/设备未配对 | 报工具/配对错误 |
| WebRTC signaling 失败 | 报 signaling 地址和 SDP 状态 |
| TURN 不可用 | 报 relay 失败；不要静默降级 |
| 屏幕/输入权限拒绝 | 报权限；不重试 |
| 帧过大/背压 | 报上限；丢帧策略写清 |

## 6. 安全边界

- ADB/iOS 配对是准入边界；rutis 信任设备放进来的连接。
- 远控服务要单独鉴权/授权；不要因为 session 连上就默认允许 input。
- 不记录屏幕内容/按键/剪贴板；日志只写元数据。
- WebRTC 的 DTLS 身份不替代 session `Hello` id。

## 7. 测试

- Android：`adb forward` + `link/tcp` 跑通 session；设备插件 `<id>:<plugin>`。
- iOS：`iproxy` + `link/tcp`（有 macOS/Xcode 时）；文档写限制。
- WebRTC：loopback 原型；signaling/TURN 写清。
- 远控：screen/input 最小实现；权限拒绝、背压、卸载。
- 断线重连/设备拔出：session 卸载，错误清楚。
- CI 不依赖真实设备；手工 e2e 步骤写进文档。

## 8. 非目标

- 不把 RDP/VNC/SPICE 做成通用 link；RDP DVC 只作为“应用协议里的例外”提一句，不是 link 层。
- 不重新实现 RDP/VNC 编解码。
- 不先做完整远控产品。
- 不把 ADB/WebRTC 身份映射成 session identity。
- 不把 screen/input 塞进 `open(far)`。

## 9. 开放问题

- O1：Android 运行时如何跑在设备上？复用 `design-android-runtime` 还是单独的 app。
- O2：iOS 非越狱设备的可行路径（App 内嵌 rutis 运行时 / Network Extension）。
- O3：WebRTC signaling 默认用哪个 link。
- O4：screen/input 服务的最小 API 是否需要视频编码（H.264/VP8）还是先 JPEG 帧。

## 关联

- issue [#8](https://github.com/arcships/rutis-network/issues/8)
- [契约设计稿](01-link-plugin-contract.md)
- [TCP bridge 设计稿](06-link-tcp.md)
- [iroh 设计稿](09-link-iroh.md)
- rutis `docs/design-android-runtime-2026-10-12.md`
