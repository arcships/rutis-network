# 通过 WSS 连接两个 rutis 宿主

[English](README.md)

两端都安装 `link-websocket`。B 运行 `go:link-websocket-listen`，A 运行 `go:link-websocket-dial`。两行各自将通过准入的 WebSocket 直接挂到本机的 `rutis/sessions`。连接建立后，两端都可以调用对方的服务。首版使用显式配置的入口。

## 配置两端

1. 从仓库根目录构建，将二进制放入两端 `runtimes.go.dir` 指定的目录。

   ```sh
   mkdir -p plugins/go
   (cd link/websocket && go build -o ../../plugins/go/link-websocket ./cmd/link-websocket)
   ```

2. B 使用[监听配置](example/listen.json)，指定绑定地址和路径，准备与 B 的公开域名匹配的 TLS 证书链和私钥。为这对节点生成随机 bearer token，例如运行 `openssl rand -hex 32`。将同一个 token 写入两台宿主各自受保护的文件，文件末尾可保留一个换行。
3. A 使用[拨号配置](example/dial.json)，WSS URL 填 B 证书对应的域名和入口路径。私有 CA 的 PEM 文件填在 `ca`，省略时使用系统信任根。A 验证 B 的证书后，在 HTTP Upgrade 时出示 token。B 校验 token 并协商 `rutis.5`，然后建立会话。
4. 将各自的清单保存为项目目录中的 `rutis.json`，运行 `rutis-host run rutis.json`。每条监听行同时接入一个连接，占用期间第二个已授权拨号方收到 HTTP 503。卸载行会关闭通道。更换凭据文件后，重新加载对应行。

透明中继转发字节时，TLS 在 WSS 监听端终止。如果反向代理终止了 WSS，它可以读取 bearer token 和会话内容。持有配对 token 即可申请接入。监听端私钥保留在本机。

一个 rutis 消息对应一个 UTF-8 WebSocket 文本消息，上限为 16 MiB。两端每 10 秒发送一次 WebSocket Ping。即使没有业务消息，对端只要回复 Pong 就能保持连接。读操作持续 30 秒未收到消息、Ping 或 Pong 时，插件会关闭会话，监听端可以接入新连接。

断开后，拨号端从 0.5 秒开始退避重连，上限为 30 秒。证书校验失败或 HTTP 认证被拒后等待 30 秒。握手协议不兼容时，拨号任务停止，重新加载行后再试。卸载拨号行也会取消正在等待响应的 HTTP Upgrade。

## 验证

从仓库根目录执行以下命令。集成测试在两条 Go 插件行之间执行双向 RPC，验证监听行重载后的重连，再用宿主参考拨号器验证与 Go 监听器的协议互通。

```sh
npm ci --prefix link/tcp/node
make integration-websocket
```
