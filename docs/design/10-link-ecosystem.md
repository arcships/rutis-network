# 链接层插件工程与发布方案

[English](10-link-ecosystem.en.md) · [Issue #10](https://github.com/arcships/rutis-network/issues/10)

草案，2026-11-27。兼容基线为 rutis 0.23.0、会话协议 5、插件 API 3。目录调整、TS 迁移和第一版开发及交付验收已在 #23 合并。以下选型用于后续实现和模板。

## 范围和优先级

**第一批支持 Go 和 TypeScript。** Go 插件作者需要可复用的 bridge，部署者需要宿主能加载的二进制。Node 参考插件和浏览器适配器使用 TypeScript 开发。浏览器应用加载编译生成的 ESM，使用 TypeScript 的集成方同时获得从实现生成的类型声明。所有宿主使用语言无关的 `rutis.json` 行配置。[TCP 接入](06-link-tcp-integration.md) · [WebRTC 接入](08-link-webrtc.md)

当前 `link/` 下有七个独立 Go module，各自声明工具链要求。TCP 的私有 npm 工程已有严格类型检查，WebRTC 的私有工程已能构建浏览器 ESM。已有 Rust listen 示例继续用 Cargo 验证。Python、Rust 的新模板以及 Swift、Kotlin 支持进入后续批次，按实际适配器需求排期。[TCP 工程](../../link/tcp/node/package.json) · [WebRTC 工程](../../link/webrtc/web/package.json) · [Rust 示例](../../examples/rust-listen/Cargo.toml)

#1 定义传输交接与一致性契约。#10 负责把这些约定落实到开发命令、CI、可安装产物、模板和目录。适配器处理传输、准入、重连。会话处理远端注册的身份、服务发现和 RPC。下面的命令名、模板目录、标签和发布文件都是拟交付内容。[链接层契约](01-link-plugin-contract.md)

## 前置阶段 · 整理目录，迁移 TypeScript 源码

以 `link/<transport>/` 为适配器的维护单位。Go 源码、`go.mod` 和 `cmd/` 沿用现有位置，其他插件依赖的 Go 导入路径及发布标签均以这个目录为基准。将已有的两个 npm 工程各设为可独立运行的子目录。

| 现有代码 | 拟定布局与迁移范围 |
| --- | --- |
| TCP 的 `bridge.ts`、`dial.ts` | 两者已经是 TS。实现移入 `link/tcp/node/src/`。原路径提供薄的再导出入口，保留 `dial.ts` 的默认插件导出和 `bridge.ts` 的命名及类型导出，现有宿主行可以继续加载。 |
| WebRTC 的 `browser/` 运行时代码和 `signaling.mjs` | 迁入 `link/webrtc/web/src/browser/`，使用严格 TS。覆盖通道、连接状态、页面端点、平台适配和 HTTP 信令。浏览器入口 `index.mts` 导出 `createWebRtcLink`、`createPageEndpoint` 和 `httpSignaling`。 |
| WebRTC 的 `pairing.mjs` | 迁为 `link/webrtc/web/src/node/pairing.ts`，由示例服务器和测试使用。浏览器入口只导出客户端 API，配对服务使用的 Node 加密模块仅供服务端加载。两端实际共用的信令类型可放在 `src/shared/`。 |
| 构建、示例和测试 | 构建入口放入包内 `scripts/`，同步修改 npm 脚本及浏览器构建的别名和注入路径。示例保留 `node example/server.mjs` 的启动方式。构建和示例脚本、Node 测试及通过路径挂载的宿主夹具可以继续使用手写 `.mjs`。测试和中英文指南随迁移更新引用。 |

迁移 PR 先在各包内提供严格类型检查。浏览器目标使用 DOM 类型，Node 目标使用 NodeNext 和 Node 类型。为固定版本 rutis 的 session、codec 构建别名描述实际调用的接口。HTTP 和 RTC 输入沿用运行时校验。tsdown 从 TS 入口构建 `dist/link-webrtc.mjs`，供普通 JS 页面直接导入。同一入口由 `tsc --emitDeclarationOnly` 生成声明，供第三阶段打包。[tsdown 平台目标](https://tsdown.dev/options/platform) · [TypeScript ESM 扩展名](https://www.typescriptlang.org/docs/handbook/release-notes/typescript-4-7.html#new-file-extensions)

**迁移验收**要求从干净检出完成两个包的类型检查、现有单元测试及宿主集成测试。用原有 `dial.ts` 行完成 TCP RPC。按现有命令启动 WebRTC 示例，让页面从原 URL 导入三个公开函数，跑通浏览器互连、原生端与浏览器 RPC 及强制 TURN 中继。完成这一阶段后，再将命令接入仓库级检查。

### 开发时运行与打包

原版 rutis-host 0.23.0 的 `rutis-host dev <目录>` 已经提供两种开发循环。Node 项目从源码加载，修改源码时重载受影响的行。Go 项目在源码变更后自动编译，编译成功才用新二进制重启对应运行时。在当前 TCP 和 WebRTC 的混合目录实测，宿主先识别 `package.json`，随后因缺少 Node 入口而退出。因此将 npm 清单、锁文件和依赖它们的测试一起放进下表的子工程。更新跨包夹具引用、安装命令及中英文指南。[rutis 0.23 开发模式](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-host/src/dev.rs) · [宿主的项目与入口识别](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-host/src/languages/mod.rs)

| 目标 | 开发入口与修改后的行为 | 一次性构建与打包 |
| --- | --- | --- |
| Go 插件（先以 TCP 验收） | 从仓库根运行 `rutis-host dev link/tcp`。开发项目用 `rutis.dev.json` 指定行和回环对端所需配置。保存 Go 源码后由宿主重编译、重启插件运行时。 | 从模块内用 `go build` 构建 `./cmd/link-tcp`。第三阶段把同一二进制打入平台归档，在独立宿主的 `runtimes.go.dir` 下加载。 |
| TCP 的 Node/TS 参考插件 | 在 `link/tcp/node/` 执行 `npm run dev`，由 tsdown 监听源码。成功构建后原子替换挂载的 ESM 入口，间接导入的 TS 文件发生变化也会触发固定版本的 `rutis-host dev` 重载该行。 | `npm run build` 检查类型并输出可加载的 ESM 和声明到包内 `dist/`。`npm pack` 通过 `prepack` 从干净源码构建本地 tarball，安装后由宿主按包名挂载。构建时保留的外部导入须声明为运行时依赖。TCP 参考包继续私有。 |
| WebRTC 浏览器端 | 在 `link/webrtc/web/` 执行 `npm run dev`，启动回环示例及同源信令。tsdown 监听依赖，成功后示例提供新模块，由 Vite 刷新开发页面。开发服务器按请求提供最新 HTML 和成功构建的模块。修改 Node 配对代码会重启示例服务。 | `npm run build` 从同一入口生成浏览器 ESM 和类型声明。`npm pack` 构建并打包 `web/dist/` 内的发布文件。构建同时将同一 ESM 提供到原示例路径 `link/webrtc/dist/link-webrtc.mjs`。 |

在前置迁移阶段打通本地开发和一次性构建，第二阶段在临时项目验证，第三阶段才发布。开发验收先完成一次 RPC，再修改一处可观察的行为，等宿主重载或浏览器刷新后重新连接并调用，验证返回新结果。重载传输行或刷新页面会结束它持有的连接，重新建连遵循适配器的配置。浏览器构建失败时显示错误并继续提供上一次成功产物，修复后恢复自动刷新。打包验收从仓库外安装产物，仅凭运行时依赖完成相同 RPC。两个模板都要演练这两条路径。

### TypeScript 工具链与测试命令

两个包和 Node 模板使用 npm 锁文件、严格 TypeScript 和 tsdown。Node 包把 rutis SDK 保留为运行时依赖，浏览器包将固定版本的运行时适配打入单个 ESM。类型检查独立于打包。浏览器示例开发时使用 Vite。发布包通过已有的 exports 提供 ESM 和声明。

| 包内命令 | 执行工具与验收 |
| --- | --- |
| `npm run test:unit` | Vitest 运行 TCP 分帧、WebRTC 通道、配对和端点用例，可在本机独立执行。 |
| `npm run test:integration` | `node:test` 启动真实 rutis 宿主。WebRTC 用 Playwright 验证 ICE 直连、认证 TURN 中继和凭据续期。SSH 与隧道集成同样使用 `node:test`。 |
| `npm run test:delivery` | `node:test` 验证源码重载，在仓库外安装 `npm pack` 生成的包、编译消费端并完成真实 RPC。 |
| `npm test` | 运行单测和集成测试。CI 可单独执行上述命令并分别报告结果。 |

## 第一阶段 · 统一格式和本地检查

- 从仓库根提供 `fmt`、`fmt-check`、`check` 三个入口。只有 `fmt` 写回源文件。`fmt-check` 列出需要格式化的文件并以非零状态退出。`check` 复用格式检查，再做静态检查和快速测试。
- Go 对受版本控制的 `.go` 文件使用 `gofmt -w` 写回，在检查模式使用 `gofmt -l`，发现输出即失败。仓库根的 `.go-version` 固定开发与 CI 使用的 Go 工具链为 1.27.4，gofmt 也取自该工具链。按各插件的 `go.mod` 分别执行 `go vet` 和单元测试。模块的 `go` 声明继续表达下游的最低版本要求。开发者可用本地工作区联调，共享 bridge 的发布依赖在第三阶段验证。
- 为手写 `.ts`、`.mts` 和 `.mjs` 固定 Prettier 版本与规则，跳过依赖和构建产物。沿用 TCP 参考工程的 Node 22.18 最低版本，将包内的严格 TS 配置提取为共享基线，分别检查浏览器和 Node 目标。格式检查与 `tsc --noEmit` 分开报告。首次格式整理单独提交，便于审查。

**验收时**，同一份检出可以从根目录检查全部七个 Go 模块及两个 JS/TS 工程。故意改坏一处格式时，检查列出文件且工作树内容不变。运行 `fmt` 后重查通过。分别在 TCP 参考插件和 WebRTC 浏览器实现中引入类型错误，根目录的类型检查应能指出相应文件。

## 第二阶段 · 把本地检查接入 PR 的 CI

依赖第一阶段的根命令。Linux 为首个必跑平台。在全新 runner 上使用仓库固定的 Go 工具链，按锁文件执行 `npm ci`，把检查拆为可单独查看的任务。

| 任务 | 执行内容 | PR 上的通过条件 |
| --- | --- | --- |
| 格式、类型与构建 | 复用 `check`，构建各 Go 插件入口及 WebRTC 浏览器模块，检查仓库内文档链接 | 每个模块和包都有结果，失败能定位到路径 |
| Go 与 TS 一致性 | 两端实现执行下表中适用的 TCP 用例。支持竞态检测的 Go runner 执行 `go test -race` | 两个实现均满足相同断言，报告列明适用范围 |
| 宿主和浏览器集成 | 准备匹配版本的 `rutis-host`、Chromium、Firefox 和回环 TURN，运行双向 RPC、强制中继及短凭据续期 | 必跑用例实际执行。缺少所需工具或用例被跳过时，该任务失败 |

基础检查每个 PR 都跑。集成任务在相关插件、共享 bridge、夹具或 CI 配置变化时运行。主分支运行完整的无公网账号矩阵。需要真实 tailnet 或供应商账号的场景单列为手动验收，记录平台及结果。保留 Rust 示例的 `cargo test --locked` 验证。[现有测试入口](../guide/link-plugins.md) · [WebRTC 集成用例](../../link/webrtc/web/test/hosts.test.mjs)

**验收时**，一个干净的 PR 能分别看到快速检查和集成结果。修改共享 TCP bridge 时，矩阵覆盖使用它的插件，并通过本地工作区验证本次修改。第三阶段另用已发布依赖验证安装。一次格式错误和一次缺少必跑宿主的演练都能得到对应的失败结果。

## 第三阶段 · 发布插件，并从仓库外安装

依赖第二阶段的构建和测试。先用 TCP 打通 Go 的两种交付，再以 WebRTC 打通 TypeScript 消费端交付。

| 使用者 | 交付与版本 | 从干净环境验收 |
| --- | --- | --- |
| Go 适配器作者 | 发布 `github.com/arcships/rutis-network/link/tcp`。子模块标签采用 `link/tcp/vX.Y.Z`，随后把 SSH 的 `v0.0.0` 换成已发布版本，移除指向 `../tcp` 的 `replace`，并更新 gRPC、WebRTC、Tailscale 固定的 TCP 伪版本 | 在仓库外、`GOWORK=off` 的临时模块中按版本引入 bridge 并构建。运行 `go install github.com/arcships/rutis-network/link/tcp/cmd/link-tcp@vX.Y.Z`（替换为实际版本），验证无本地目录依赖 |
| 宿主部署者 | 从同一标签构建 `link-tcp`。GitHub Release 提供含插件名、版本、OS、架构的归档、SHA-256 校验和、rutis 兼容版本及最小配置。首个验收目标为 Linux/amd64 | 下载并校验归档，将二进制放入 `runtimes.go.dir` 指定目录，用原版宿主加载 `go:link-tcp-dial` 并通过一次远端 RPC |
| 浏览器 TypeScript 集成方 | 用 `exports` 分别指向浏览器 ESM 和从同一 TS 源码生成的 `.d.mts` 入口，并打包它引用的全部声明。覆盖 `createWebRtcLink`、`createPageEndpoint`、`httpSignaling`，以及配置、状态、信令和回调。TCP 的 TS 参考工程继续用于互通测试 | 用 `npm pack` 生成的包在独立 TS 项目安装，按包名导入并通过严格类型检查，再由真实浏览器从该包完成一次 RPC |

插件和 npm 包各自使用语义化版本，发布说明单列经过验证的 rutis 次版本、协议和运行平台。子模块标签使 Go 能解析相应目录的版本。普通 PR 构建候选产物并演练安装。维护者从通过 CI 的提交发起正式发布，外部安装验证使用相同的版本和产物。公开分发前补齐拟采用的 MIT LICENSE 并确认版权归属，公开 npm 发布另需确认包名与 scope 权限。新平台通过下载、加载和 RPC 验收后再加入发布矩阵。[Go 模块版本规则](https://go.dev/ref/mod#vcs-version) · [Go 插件安装示例](06-link-tcp-integration.md) · [npm pack](https://docs.npmjs.com/cli/v11/commands/npm-pack)

## 第四阶段 · 交付 Go 和 TypeScript 脚手架

基于第二阶段的一致性测试和第三阶段验证过的交付方式，在本仓库提供两个可复制模板：

| 模板 | 作者拿到什么 |
| --- | --- |
| `templates/go/link-echo/` | 独立 `go.mod`，依赖已发布的 TCP bridge，提供适配器、`cmd/link-echo` 入口、配置示例和测试 |
| `templates/typescript/link-echo/` | 独立的 Node ESM 包、npm 锁文件、严格 `tsconfig`、Prettier、tsdown、Vitest 单测及 node:test 集成与交付命令、位于 `src/` 的插件入口、配置示例和 `test/` 用例。从受测的 TCP 参考源码带入 bridge，复制后能独立构建 |

两个模板都通过回环 TCP 和临时 mTLS 凭据接入测试服务，使用 `rutis/sessions.open(far)` 交接连接。README 包含安装、修改传输、启动、错误与限制。CI 从模板复制到仓库外，修改插件名、安装依赖、构建、启动宿主并调用一次 echo，再执行共享一致性用例。该流程就是本阶段的验收。模板成功后再扩展语言。[TCP 参考实现](../../link/tcp/bridge.ts) · [插件开发指南](../../.agents/skills/rutis-link-plugin/SKILL.md)

## 一致性与管理边界

Go 和 TS 使用相同的用例名和预期结果，分别用 Go 测试和 Vitest 实现单元用例。字节样本和大小边界可共享为数据。慢写、断连、卸载由各语言夹具控制时序。

| 用例 | 断言 |
| --- | --- |
| 消息边界与顺序 | 1000 条消息按序交付，含空消息。TCP 单帧 16 MiB 可通过，超过一字节时连接干净关闭 |
| 背压与异步写 | 慢接收方会阻塞发送，发送方等待写入完成。bridge 自有队列有界，恢复后消息齐全。rutis 0.23 的非 Rust 会话总出站队列仍由宿主管理 |
| EOF 与卸载 | EOF 映射为 `near.end(done)` 并关闭双向连接。会话卸载关闭传输，清理只注册、执行一次 |
| 准入、身份与监听 | 准入失败时 `open` 调用次数为零。用宿主验证远端服务寻址和 `<id>:<plugin>` 命名。对支持多连接的监听器，每条已接纳连接对应一个会话 |
| 同 ID 接替 | 由 Rust 握手示例验证。Go/TS 在 0.23 中无法从 `open` 取得已握手的对端 ID。同 ID 接替及该 ID 的状态字段列为不适用 |
| 错误 | 截断、超限、连接重置按稳定错误码断言。日志不含凭据 |

组合仍通过 `rutis.json` 的行、会话和服务名完成。通过 `rutis/loader` 增删行。每个插件声明自己的配置 schema，可提供行级控制服务，报告连接阶段、可观测的直连或中继路径、上次错误和重试次数。业务插件按服务名调用。凭据、重连和可选的 `doctor` 工具归相应适配器管理。WebSocket 使用核心已有能力，普通 HTTP 可承载信令。[链接层契约与宿主限制](01-link-plugin-contract.md)

## 目录与后续覆盖

沿用中英文插件目录，按适配器记录提供的通道、准入方式、语言、部署前提、场景、优先级（T1/T2/T3）、状态（计划、原型、可用），并链接 issue、设计、示例及经验证的安装方式。SSH、Tailscale、Cloudflare Tunnel 各保留至少一个可复制的配置示例。第三方适配器按实际通过的一致性用例标注覆盖范围。[插件目录](../guide/link-plugins.md)
