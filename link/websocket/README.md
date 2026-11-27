# Connect two rutis hosts over WSS

[中文](README.zh-CN.md)

Install `link-websocket` on both hosts. B runs `go:link-websocket-listen`. A runs `go:link-websocket-dial`. Each plugin attaches its admitted WebSocket directly to its own `rutis/sessions` service. Either host can then call services from the other. This version uses an explicitly configured endpoint.

## Configure the two hosts

1. Build from the repository root. Install the resulting binary in the directory configured by `runtimes.go.dir` on both hosts.

   ```sh
   mkdir -p plugins/go
   (cd link/websocket && go build -o ../../plugins/go/link-websocket ./cmd/link-websocket)
   ```

2. On B, use the [listener manifest](example/listen.json). Set the bind address and path, and provide a TLS certificate chain and key for B's public DNS name. Generate a random bearer token for this pair (for example, `openssl rand -hex 32`). Provision the same token in a protected file on both hosts. The file can end with one newline.
3. On A, use the [dialer manifest](example/dial.json). Set the WSS URL to B's certified name and path. For a private CA, set `ca` to its PEM file. Omitting `ca` selects system roots. A sends the token during HTTP Upgrade after verifying B's certificate. B checks the token and negotiates `rutis.5` before opening a session.
4. Save each manifest as `rutis.json` in its host's project and run `rutis-host run rutis.json`. Each listener row admits one active connection. A second authorized dialer receives HTTP 503 while the slot is occupied. Removing the row closes the channel. Reload a row to pick up changed credential files.

TLS ends at the WSS listener when a relay forwards bytes transparently. A reverse proxy that terminates WSS can read the bearer token and the session traffic. Possession of the pair's token grants connection admission. Keep the private key on the listener.

Each rutis message occupies one UTF-8 WebSocket text message, up to 16 MiB. Both ends send a WebSocket Ping every 10 seconds. An idle peer that answers with Pong stays connected. If a read waits 30 seconds without a message, Ping or Pong, the plugin closes the session and frees the listener slot.

Disconnects trigger redial with backoff from 0.5 to 30 seconds. An HTTP authentication refusal or certificate verification failure waits 30 seconds. An incompatible handshake stops the dial task until the row reloads. Unloading the dialer also cancels a pending HTTP Upgrade.

## Verify

Run these commands from the repository root. The integration test makes RPCs in both directions between two Go plugin rows, checks reconnect after listener reload and connects the reference host dialer to the Go listener.

```sh
npm ci --prefix link/tcp/node
make integration-websocket
```
