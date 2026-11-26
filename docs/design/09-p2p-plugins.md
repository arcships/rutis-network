# Go 版 libp2p 链接插件

[English](09-p2p-plugins.en.md)

Go 插件使用 go-libp2p 0.52.0，适配 rutis-host 0.23.0。一个二进制提供 `go:link-libp2p`，每行配置连接一个对端。拨号方按地址末尾的 Peer ID 校验对端，监听方只接受 `allowPeer` 指定的 Peer ID。双方通过 `/rutis/link/1` 打开双向流，准入成功后交给 `rutis/sessions.open(far)`。

## 双端配置

使用 Go 1.26 或更高版本，在仓库根目录构建插件。在 A、B 两端分别运行一次 `keygen`，它以仅所有者可读写的权限创建密钥文件，输出公开 Peer ID。已有密钥不会被覆盖。

```sh
(cd link/libp2p && go build -o ../../plugins/go/link-libp2p ./cmd/link-libp2p)
./plugins/go/link-libp2p keygen ./peer.key
```

将插件放入 `plugins/go`，在两台宿主的 `rutis.json` 设置 `"runtimes": {"go": {"dir": "plugins/go"}}`。将各行的 `keyFile` 改为生成的密钥文件的绝对路径，交换公开的 Peer ID。A 的 `rows` 加入拨号行，将示例地址换成 B 可达的地址。

```json
{
  "id": "to-b",
  "name": "go:link-libp2p",
  "isolate": { "office-link": "local" },
  "config": {
    "keyFile": "/etc/rutis/a.key",
    "dial": "/ip4/192.0.2.10/udp/7444/quic-v1/p2p/<B_PEER_ID>",
    "controlService": "office-link"
  }
}
```

B 的 `rows` 加入监听行。

```json
{
  "id": "from-a",
  "name": "go:link-libp2p",
  "config": {
    "keyFile": "/etc/rutis/b.key",
    "listen": "/ip4/0.0.0.0/udp/7444/quic-v1",
    "allowPeer": "<A_PEER_ID>"
  }
}
```

默认用 QUIC 直连。需要中继时，B 可加 `relay`，填中继的完整 multiaddr 并向它预约。A 的 `dial` 改为 `<中继 multiaddr>/p2p-circuit/p2p/<B_PEER_ID>`。[Go 中继示例](https://github.com/libp2p/go-libp2p/blob/v0.52.0/examples/relay/main.go)

## 插件管理

配置加载后自动启动。拨号端每次启动尝试一次，断线后用 `start()` 重拨。监听端继续接受同一对端的新连接。管理插件注入 `office-link`，并设置相同的 `"isolate": {"office-link": "local"}`。根作用域的服务会导出给对端，启用控制服务时请保留示例中的隔离配置。改地址或密钥时，编辑配置并重载该行。

| 方法 | 行为 |
| --- | --- |
| `start()` | 按当前配置拨号或监听，重复调用安全。 |
| `stop()` | 取消拨号，关闭本行的监听和会话。控制服务保留，可再次启动。 |
| `status()` | 返回当前状态、已连接的 Peer ID、直连或中继路径以及最近错误。 |

一条流挂一个会话，使用 4 字节大端长度前缀，单帧上限 16 MiB。rutis 0.23 的 Go 接口无法读取握手后的 rutis ID，也不能设置会话发送队列上限。因此每行仅接受一条活动流，旧流结束后再接受新流。[会话接口](https://github.com/arcships/rutis/blob/v0.23.0/crates/rutis-bridge/src/session/plugin/open.rs)

运行 `(cd link/libp2p && go test -race ./...)` 可验证本机 QUIC 直连、中继、拒绝未授权和重复流、分帧与停止后重启。将 rutis-host 0.23.0 放入 PATH 或通过 `RUTIS_HOST` 指定路径，再运行 `(cd link/libp2p && go test -tags rutishost -run TestTwoHosts)`，可在两个真实宿主上验证远端服务调用、停止、恢复和控制服务的隔离。
