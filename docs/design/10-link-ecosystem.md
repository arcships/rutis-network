# 链接层插件生态：模板、一致性测试、适配器目录：设计稿

状态：草案。日期：2026-11-08。基线：rutis 0.21.0。
关联 issue：[#10](https://github.com/arcships/rutis-network/issues/10)。依赖：[#1](https://github.com/arcships/rutis-network/issues/1)、#3–#6。

## 1. 目标

让第三方能复制一个能跑的 link plugin 模板，用同一套一致性测试验证，不把认证/身份/重连塞进 session；让用户能在目录里找到该用哪个 adapter。

## 2. 与 #1 的边界

- #1：契约 + 一个参考 bridge + 一致性测试的**契约**。
- #10：各语言模板、**可执行**的一致性测试套件、目录、CI、许可证、版本策略。

## 3. 模板

语言顺序：TS、Python、Rust；之后 Go、Swift、Kotlin。

```text
templates/<lang>/
  link-echo/
    src/bridge.*      # framing + rutis/sessions.open(far) + lifecycle
    src/adapter.*     # dial/listen；准入在这里
    test/             # 单元测试 + 一致性用例
    README.md         # 安装、配置、错误、限制
```

要求：

- 只依赖 `rutis/sessions`（TS/Python）或 `Session::new`（Rust）；不新增核心 API。
- `link/echo` 用 localhost TCP（只绑 `127.0.0.1`，随机端口/token）；WebSocket 复用核心 `websocket::dial` / `websocket::listen`；不提供纯 HTTP link 模板。
- 注释写清：认证在 adapter；身份来自远端 `Hello`；session 不碰 token。
- 0.21 异步 `send`、0.20 卸载顺序、错误 code 写进模板。
- 模板能 `copy → 改 adapter → 跑测试`；不要求所有语言先支持所有 adapter。
- 每个 T1 adapter 至少一个可复制示例：SSH、Tailscale、Cloudflare Tunnel；示例只依赖 `rutis.json` row + `rutis/sessions`，不引入 manager。

### 3.1 管理工具与组合

- 模板不暴露跨 link 的 manager 或全局 `connect` 服务；每个 adapter 只提供自己的 row 配置和 session。
- 组合靠 `rutis.json` row + `<link row>:<plugin>` + 服务名；业务插件不认识 link。
- 每个 adapter 可以带可选的 `status` / `doctor` / CLI，作为该 link 自己的工具；不做中心管理工具，不集中存凭据。
- row 契约要留够：配置 schema、结构化状态（connected/retrying/stopped、direct/relay、last error、`Hello` id、retry count）、可选 `status` / `doctor`、日志无 secret。`Hello` id 是 session 对面注册的 id，不是 SSH 用户/tailnet 节点/token；在 #1 O1（握手结果 API）落地前，非 Rust 的 `Hello` id 状态标为不可用/Rust-only。
- 动态增删用 `rutis/loader` / `rutis.json`，不是运行时 dial 中心；优先常驻 + 重连。

## 4. 一致性测试套件

同一套用例，跨语言执行（每语言一个 runner，共享 JSON 用例或同一套语义）。默认传输是 localhost TCP（只绑 `127.0.0.1`，随机端口/token）；WebSocket 用例复用核心能力；纯 HTTP 没有用例。

| 用例 | 断言 |
| --- | --- |
| 边界/顺序 | 1000 条消息按序到达，边界不合并 |
| 最大消息 | 16 MiB 通过；16 MiB+1 干净关闭（16 MiB 是 bridge 策略，不是 `Channel` 契约） |
| 空消息 | 长度 0 通过 |
| 背压 | 慢接收方，bridge 队列不超上限，内存不涨 |
| 异步 send | `send` 返回 Promise，session await；慢写不丢 |
| close | 远端 FIN → `near.end`；session 卸载 → 连接关闭一次 |
| EOF / close | 读 EOF 映射为 `done`；session 没有半关闭，不重复关闭 |
| 远端 id | `<id>:<plugin>` 命名 |
| listen 多连接 | 每条连接一个 session |
| 同 id 顶替 | Rust 顶替；非 Rust 不支持（`session_key` 先到先得） |
| 准入失败 | `open` 之前失败；没有 session |
| 卸载 | session 卸载后 adapter 连接关闭；cleanup 只注册一次 |
| 错误 | 截断/超长/重置有 code，无 token |

测试不依赖公网账号；SSH/Tailscale/Cloudflare 用本地 fake 或手写 e2e 步骤。

## 5. 适配器目录

`docs/guide/link-plugins.md`（及 `.en.md`）或 `docs/guide/link-plugins/`，每个 adapter 一行：

| 字段 | 内容 |
| --- | --- |
| 名称 | `link/ssh`、`link/tailscale`… |
| 给什么 | 字节流 / 消息流 / 本地 TCP / 公网入口 |
| 准入 | SSH host key / tailnet ACL / Access token / mTLS |
| 语言 | TS / Python / Rust / Go / … |
| 部署前提 | 系统 sshd、tailscaled、cloudflared、ADB、VPS… |
| 场景 | 个人 GPU、home lab、手机、团队… |
| 优先级 | T1/T2/T3 |
| 状态 | 计划 / 原型 / 可用 |
| 链接 | issue、设计稿、示例 |

不把“计划中”写成“已支持”。

## 6. CI

- 模板各语言编译/测试；
- 一致性测试至少一个语言接入 CI，其他语言逐步；
- 文档链接检查（`node tools/check-md-links.mjs`，如果 rutis-network 引入）；
- 不依赖公网账号；
- 矩阵：Linux 必跑；macOS/Windows 按语言能力。

## 7. 许可证与版本

- LICENSE：与 rutis 一致的 MIT。
- 版本策略：adapter 跟 rutis 次版本走；模板声明最低 rutis 版本（0.21.0 起）。
- 破坏性变更（如 0.19.3 的 `sessions` → `rutis/sessions`）在 README/迁移说明里写清。
- 第三方 adapter 用一致性测试分级，不承诺质量。

## 8. 非目标

- 不新增核心 trait/API。
- 不要求所有语言同时支持所有 adapter。
- 不做插件市场；先文档 + 模板。
- 不做跨 link 的中心管理工具 / 全局 `connect` 服务 / 中心凭据库；管理工具是每条 link 可选的、外部的。
- 不重复 #1 的契约和参考 bridge。
- 不把 CI 依赖公网服务。

## 9. 开放问题

- O1：一致性用例用 JSON DSL 还是每语言手写？JSON 便于跨语言，但表达力有限。
- O2：模板放本仓库还是独立 repo？先本仓库 `templates/`。
- O3：目录用单页还是每 adapter 一页？先单页表格，超过 10 个再拆。
- O4：许可证年份/版权人写谁（与 rutis 保持一致）。

## 关联

- issue [#10](https://github.com/arcships/rutis-network/issues/10)
- [契约设计稿](01-link-plugin-contract.md)
- [SSH 设计稿](03-link-ssh.md)
- [Tailscale 设计稿](04-link-tailscale.md)
- [Cloudflare 设计稿](05-link-cloudflared.md)
- [TCP bridge 设计稿](06-link-tcp.md)
- [SKILL.md](../../.agents/skills/rutis-link-plugin/SKILL.md)
