# feat(link): 链接层插件生态：模板、一致性测试、适配器目录

状态：已发布为 [#10](https://github.com/arcships/rutis-network/issues/10)。优先级 P2。依赖契约 #1 与至少一个 T1 插件。设计稿：[PR #20](https://github.com/arcships/rutis-network/pull/20)。

版本基线：rutis 0.21.0（2026-11-05），会话协议 4，插件 API 3。

## 问题

如果每个语言的 link plugin 都自己实现 framing、重连、listen 顶替、错误输出，生态会分叉；作者也会把认证/身份塞进 session。需要一个可复制的骨架和一致性测试。

同时，用户需要知道“我该用哪个 link plugin”：SSH/Tailscale/Cloudflare/frp/gRPC/ADB/iroh……没有目录和场景说明，插件再多也找不到。

## 与 #1 的边界

- **#1**：契约文档 + 一个参考 bridge + 一致性测试的**契约**。不铺开各语言模板，不做目录和 CI。
- **#10**（本 issue）：各语言模板、可执行的一致性测试套件、适配器目录、CI、许可证与版本策略。

## 范围

1. 各语言模板（先 TS / Python / Rust，Go / Swift / Kotlin 后续）：
   - 以 `.agents/skills/rutis-link-plugin/SKILL.md` 为 agent 指南，模板与它保持一致；
   - 只依赖 `rutis/sessions` 服务 / `Session::new(channel)`；
   - 一个 `link/echo` 或 `link/tcp` 最小实现；优先用 localhost TCP，WebSocket 复用核心已有能力；不新造协议，也不把纯 HTTP 请求/响应当 link；
   - adapter + bridge 的文件结构；
   - 注释里写清认证在 adapter、身份来自远端 `Hello`、session 不碰 token；
   - 0.21 异步 `send`、0.20 卸载顺序、错误 code 写进模板注释。
2. 一致性测试套件：
   - 消息边界、顺序、背压、异步 `send`、close、half-close；
   - 超长消息；
   - 远端 `Hello` id 命名 `<id>:<plugin>`；
   - listen 多连接 + 同 id 顶替（Rust；非 Rust 写明限制）；
   - `open` 之前认证失败不创建 session；
   - session 卸载 → channel 关闭；
   - 同一套用例跨语言跑。
3. 适配器目录 `docs/guide/link-plugins.md` 或 `docs/guide/link-plugins/`：
   - 每个 adapter 写：给什么、准入、语言支持、部署前提、场景、优先级；
   - 标注 T1/T2/T3；
   - 不把“计划中”写成“已支持”；
   - 链接到对应 issue 和设计稿。
4. CI：
   - 模板各语言编译/测试；
   - 一致性测试在至少一个语言上跑；
   - 文档链接检查；
   - 不依赖公网账号（SSH/Tailscale/Cloudflare 用本地 fake 或手写 e2e 步骤）。
5. 仓库治理：
   - 加 LICENSE（与 rutis 一致的 MIT）；
   - 版本策略：本仓库的 adapter 跟 rutis 的次版本走，模板声明 `rutis/sessions` 最低版本；
   - issue/PR 模板（可选）。

## 非目标

- 不新增核心 trait / API。
- 不要求所有语言同时支持所有 adapter。
- 不把 adapter 目录做成插件市场；先做文档和模板。
- 不承诺第三方 adapter 的质量；用一致性测试分级。
- 不重复 #1 的契约文档和参考 bridge。

## 验收

- [ ] TS/Python/Rust 模板可复制、可运行、只依赖 `rutis/sessions`；在 0.21.0 上跑通。
- [ ] `link/echo` 在 localhost TCP 上可跑；WebSocket 复用核心 `websocket::dial` / `websocket::listen`；没有纯 HTTP link 模板。
- [ ] 一致性测试套件覆盖上述行为，至少一个语言接入 CI。
- [ ] `docs/guide/link-plugins.md`（及 `.en.md`）有 adapter 目录，T1/T2/T3 和场景清楚。
- [ ] 每个 T1 adapter 至少有一个可复制示例（SSH/Tailscale/Cloudflare）。
- [ ] 仓库有 LICENSE，版本策略写清。
- [ ] 文档链接检查和模板测试通过。

## 关联

- 设计稿：[PR #20](https://github.com/arcships/rutis-network/pull/20)
- [链接层 link plugin 契约](01-link-plugin-contract.md)
- [`link/ssh`](03-link-ssh.md)
- [`link/tailscale`](04-link-tailscale.md)
- [`link/cloudflared`](05-link-cloudflared.md)
- [`link/tcp` 与反向隧道](06-link-tcp-and-reverse-tunnels.md)
- [0.21 发布说明](https://github.com/arcships/rutis/blob/main/docs/releases/0.21.0.md)
