# 通过 SSH 连接 rutis

[English](03-link-ssh.en.md) · [议题 #3](https://github.com/arcships/rutis-network/issues/3)

`go:link-ssh-dial` 适用于已经有 SSH 入口、远端运行 rutis TCP listener 的部署。基线为 rutis 0.23.0、会话协议 5、插件 API 3。Go 插件在一条 SSH 连接上打开一个 `direct-tcpip` 通道，经 mTLS 验证后把字节流交给现有 TCP bridge。每次连接对应一个 rutis 会话。

```text
调用方 → go:link-ssh-dial → SSH 服务端 → 127.0.0.1:7443 → go:link-tcp-listen
```

## 部署

从仓库根目录用 Go 1.26 或以上版本构建，将两个二进制分别放在拨号端和接收端配置指定的运行时目录。

```sh
mkdir -p plugins/go
go -C link/ssh build -o ../../plugins/go/link-ssh ./cmd/link-ssh
go -C link/tcp build -o ../../plugins/go/link-tcp ./cmd/link-tcp
```

接收端的 `rutis.json` 示例如下。TCP listener 绑定 IPv4 回环地址。为 SSH 账号配置用户公钥，允许到目标端口的本地转发，例如在对应的 sshd `Match User` 块中设置 `AllowTcpForwarding local` 和 `PermitOpen 127.0.0.1:7443`。两者同机时只需对外开放 SSH 端口。

```json
{
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [{
    "id": "accept", "name": "go:link-tcp-listen",
    "config": { "port": 7443, "ca": "/etc/link/client-ca.pem", "cert": "/etc/link/server.pem", "key": "/etc/link/server.key" }
  }]
}
```

拨号端按实际部署替换地址、用户名和绝对路径。预先核实主机公钥并写入指定的 OpenSSH `known_hosts` 文件，非 22 端口使用 `[host]:port`。提供 `identityFile` 时读取无口令私钥。省略它时使用运行时进程的 `SSH_AUTH_SOCK`，可在本地 agent 中解锁私钥。插件直接拨号，地址填写实际主机名或 IP。只有 SSH 用户认证使用 agent，远端收到的业务流量使用 mTLS 客户端证书。

```json
{
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [{
    "id": "wire", "name": "go:link-ssh-dial",
    "isolate": { "office-ssh": "management" },
    "config": {
      "controlService": "office-ssh",
      "sshAddress": "bastion.example.com:22", "targetAddress": "127.0.0.1:7443",
      "servername": "rutis.internal", "profile": "office",
      "profiles": { "office": {
        "user": "rutis", "knownHosts": "/etc/link/known_hosts", "identityFile": "/etc/link/id_ed25519",
        "ca": "/etc/link/server-ca.pem", "cert": "/etc/link/client.pem", "key": "/etc/link/client.key"
      } }
    }
  }]
}
```

`sshAddress` 从拨号端访问，`targetAddress` 从 SSH 服务端访问。两者均使用 `host:port`，IPv6 写作 `[地址]:端口`。`servername` 验证目标 listener 的证书，默认取 `targetAddress` 的主机部分。SSH 主机密钥验证、用户认证和目标 TLS 握手都在交给 bridge 前完成。远端插件名取决于会话 Hello 中的 ID。bridge 使用 4 字节大端长度前缀，单帧上限为 16 MiB，大数据按应用分块传输。[TCP 帧与分块约定](06-link-tcp.md)

## 管理连接

集成方使用任意 rutis SDK，在管理插件的行上声明 `"inject": ["office-ssh"]` 和 `"isolate": {"office-ssh": "management"}`，再取得该服务。将它保留在本地隔离作用域，根作用域的普通服务会导出到对端。SSH 行持有连接。管理插件或业务插件可以独立重启。

| 调用 | 行为 |
| --- | --- |
| `configure(patch)` | 接受 `sshAddress`、`targetAddress`、`servername`、`profile`、`reconnect`。省略的字段保留，`servername: null` 恢复默认值。凭据只能选择部署中列出的 profile。运行时切换目标或 profile 会先关闭旧连接。无效或相同的补丁保留当前连接。 |
| `start()` | 接受当前配置并开始连接。重复调用复用进行中的尝试。失败后可显式重试。 |
| `stop()` | 取消拨号、握手或退避，关闭当前连接，并等待传输清理完成。之后修改配置只影响下次启动。 |
| `status()` | 返回 `desired`、`phase`、`config`、`cursor`、`retryCount`。已知时还包含 SSH 外层 socket 的 `localAddress`、`remoteAddress` 以及 `lastError`、UTC 时间 `retryAt`。 |
| `waitForChange(cursor)` | 游标匹配时等待变化，否则立即返回最新快照。行重载后重新取得服务，旧游标可用于读取新实例。 |

受管行默认自动启动和重连。配置不完整时处于 `awaiting-config`，补齐后开始拨号。`autoStart: false` 让集成方决定何时启动。静态行省略 `controlService`，提供完整地址和 profile，默认在加载时拨号一次。运行时编辑仅在该行实例内有效。需要在不同对端之间迁移 `wire:<plugin>` 行时，先等待 loader 移除旧传输行，再以同一行 ID 创建新传输。[0.23.0 的远端插件迁移](06-link-tcp-integration.md)

`connected` 表示通道经本端 TLS 验证并交给会话。可通过业务 RPC 确认远端服务就绪。断线后，每次重试都会重新读取凭据并建立新连接。普通网络错误从 500 毫秒开始指数退避，上限 30 秒，连接持续 60 秒后重置。凭据文件错误、SSH 握手失败（包括用户认证失败）和证书拒绝等待 30 秒。主机密钥拒绝、转发被禁止和不兼容的 SSH/TLS 协商会进入 `failed`。修正后调用 `start()`。日志记录地址和错误码，如 `ssh-host-key-rejected` 或 `ssh-forward-denied`。切换重试开关保留健康连接。

## 验证与后续模式

从仓库根目录运行下列命令。宿主测试使用 Node 22.18+、OpenSSL、OpenSSH 的 `ssh-keygen`、`ssh-agent`、`ssh-add` 和可在本机运行的 `sshd`（可用 `SSHD` 指定路径）。测试会生成临时密钥并启动回环服务。

```sh
go -C link/ssh test -race ./...
npm ci --prefix link/tcp
node --test link/ssh/test/hosts.mjs
```

目前交付原生 Go 拨号。已有的外部 OpenSSH 反向转发仍可搭配 TCP 插件使用。只有 SSH 登录、远端没有 TCP listener 的部署需要单独的 stdio 命令或 subsystem 来适配 rutis。这属于后续模式。[反向转发指南](../guide/link-plugins.md)
