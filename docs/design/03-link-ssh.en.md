# Connecting rutis over SSH

[中文](03-link-ssh.md) · [Issue #3](https://github.com/arcships/rutis-network/issues/3)

`go:link-ssh-dial` connects through an existing SSH server to a rutis TCP listener. It targets rutis 0.23.0, session protocol 5 and plugin API 3. The Go plugin opens a `direct-tcpip` channel, verifies the listener over mTLS and gives the stream to the existing TCP bridge. Each connection mounts one rutis session.

```text
caller → go:link-ssh-dial → SSH server → 127.0.0.1:7443 → go:link-tcp-listen
```

## Deployment

From the repository root, build with Go 1.26 or later. Place each binary in its host's configured Go runtime directory.

```sh
mkdir -p plugins/go
go -C link/ssh build -o ../../plugins/go/link-ssh ./cmd/link-ssh
go -C link/tcp build -o ../../plugins/go/link-tcp ./cmd/link-tcp
```

Configure the receiving host as shown below. The TCP listener binds to IPv4 loopback. Authorize the SSH user's public key and allow local forwarding to this port, for example with `AllowTcpForwarding local` and `PermitOpen 127.0.0.1:7443` in its sshd `Match User` block. When sshd and the listener share a machine, only the SSH port needs external ingress.

```json
{
  "runtimes": { "go": { "dir": "plugins/go" } },
  "rows": [{
    "id": "accept", "name": "go:link-tcp-listen",
    "config": { "port": 7443, "ca": "/etc/link/client-ca.pem", "cert": "/etc/link/server.pem", "key": "/etc/link/server.key" }
  }]
}
```

On the dialing host, substitute the actual addresses, SSH user and absolute file paths. Verify the SSH host key in advance and install it in the specified OpenSSH `known_hosts` file. Use `[host]:port` for a nonstandard port. Supply an unencrypted private key via `identityFile` or omit that field to use the runtime's local `SSH_AUTH_SOCK`, where the agent can unlock an encrypted key. Supply a real hostname or IP for the native dialer. The agent serves SSH user authentication. The destination uses the mTLS client certificate for admission.

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

The dialer connects to `sshAddress`. The SSH server resolves and connects to `targetAddress`. Both take `host:port`, including `[IPv6]:port`. The listener certificate must match `servername`, which defaults to the host in `targetAddress`. SSH host verification, user authentication and the target TLS handshake precede bridge handoff. The session takes the remote plugin namespace from the endpoint's Hello. The shared bridge uses a four-byte big-endian length and a 16 MiB frame limit. Applications can transfer larger payloads in chunks. [TCP framing and chunking](06-link-tcp.en.md)

## Connecting through a VPS

A provides a service and B calls it. Both dial the existing SSH port on the VPS. A's SSH account needs remote forwarding (for example `AllowTcpForwarding remote` and `PermitListen 127.0.0.1:17443`). B's account needs local forwarding (for example `AllowTcpForwarding local` and `PermitOpen 127.0.0.1:17443`). An account used for both roles needs both permissions. The VPS only runs sshd. Use `GatewayPorts no` or `clientspecified` to preserve the loopback bind. `GatewayPorts yes` forces wildcard binding.

Choose the row for your service in A's `rutis.json`. Both run from the same `link-ssh` binary. To run both together, assign distinct relay ports and local control service names.

**Receive rutis sessions directly.** A requests `listenAddress` on the VPS. The SSH plugin verifies each incoming client certificate before handing the stream to A's session service. The server certificate must match B's `servername`.

```json
{
  "id": "inbound",
  "name": "go:link-ssh-listen",
  "isolate": {
    "office-in": "management"
  },
  "config": {
    "controlService": "office-in",
    "sshAddress": "vps.example.com:22",
    "listenAddress": "127.0.0.1:17443",
    "profile": "office",
    "profiles": {
      "office": {
        "user": "rutis",
        "knownHosts": "/etc/link/known_hosts",
        "identityFile": "/etc/link/id_ed25519",
        "ca": "/etc/link/client-ca.pem",
        "cert": "/etc/link/server.pem",
        "key": "/etc/link/server.key"
      }
    }
  }
}
```

**Forward to a TCP service.** A forwards each incoming connection to `localTarget` as reached from A. For rutis, run the TCP listener shown above at port 7443 on A. That listener handles mTLS and session admission. An ordinary TCP service can also be the target and handles authentication through its own protocol.

```json
{
  "id": "forward",
  "name": "go:link-ssh-tunnel",
  "isolate": {
    "office-in": "management"
  },
  "config": {
    "controlService": "office-in",
    "sshAddress": "vps.example.com:22",
    "listenAddress": "127.0.0.1:17443",
    "profile": "office",
    "profiles": {
      "office": {
        "user": "rutis",
        "knownHosts": "/etc/link/known_hosts",
        "identityFile": "/etc/link/id_ed25519"
      }
    },
    "localTarget": "127.0.0.1:7443"
  }
}
```

In B's dial configuration above, use `sshAddress: "vps.example.com:22"`, `targetAddress: "127.0.0.1:17443"` and A's TLS server name. A's `listenAddress` takes a loopback IP and port. When sshd permits dynamic allocation, port zero asks the VPS to allocate a port. Read `status().boundAddress` and pass that address to B. Use a fixed port for a stable endpoint across reconnects.

Both receiving roles expose the controls below. Their `configure` accepts `sshAddress`, `listenAddress`, `profile` and `reconnect`. The tunnel also accepts `localTarget`. Receiving roles retry by default, including static rows. Changing an endpoint or profile, or stopping the row, closes the old bind and its active streams. After SSH reconnects the row requests a new bind. sshd reports one bind failure for both denied forwarding and an occupied port. The plugin reports `ssh-listen-failed` and applies its retry policy.

The `listening` phase and `boundAddress` indicate that the VPS accepted the bind. `connections` counts streams attached to a rutis session or connected to the TCP origin. Individual stream errors update `lastClientError` and leave the listener running. Each row handles up to 64 simultaneous streams, including pending admission. Go's rutis 0.23.0 session API cannot replace an old session by remote ID. [Session lifecycle](01-link-plugin-contract.en.md)

## Managing the connection

Any rutis SDK can consume the control service. Add `"inject": ["office-ssh"]` and `"isolate": {"office-ssh": "management"}` to the local management row and acquire the service by name. Keep it in that local isolation scope because ordinary root services are exported to peers. The SSH row owns the connection across manager and business plugin restarts.

| Method | Behavior |
| --- | --- |
| `configure(patch)` | The dialer accepts `sshAddress`, `targetAddress`, `servername`, `profile` and `reconnect`. Omitted fields retain their values. Use `servername: null` to restore its default. Profiles select credentials declared in the deployment. A live endpoint or profile change closes the old connection first. Invalid or identical patches preserve the connection. |
| `start()` | Starts an attempt with the current settings. Repeated starts share an active attempt. An explicit start retries a failed connection. |
| `stop()` | Cancels dialing, negotiation or backoff, closes the live connection and waits for transport cleanup. Edits after stopping take effect on the next start. |
| `status()` | Returns `desired`, `phase`, `config`, `cursor` and `retryCount`. When available, it includes the outer SSH socket's `localAddress` and `remoteAddress`, the `lastError` code and the UTC `retryAt` timestamp. |
| `waitForChange(cursor)` | Waits if the cursor matches, otherwise returns the latest snapshot immediately. Reacquire the service after row reload and supply the old cursor to read the new instance. |

Managed rows start and retry by default. An incomplete configuration yields `awaiting-config` until the required fields arrive. Set `autoStart: false` for manual startup. Static dial rows omit `controlService`, require both addresses and a profile, and dial once on load by default. Runtime edits belong to the current row instance. To migrate `wire:<plugin>` rows between peers, wait for loader removal and create the transport under the same row ID. [Remote plugin handoff in 0.23.0](06-link-tcp-integration.en.md)

`connected` means the local TLS handshake and session handoff completed. A business RPC confirms service readiness. Each retry rereads credentials and opens a fresh connection. Transient network failures retry after 500 ms, doubling to 30 s and resetting after 60 s connected. Invalid credentials, SSH handshake failures (including user authentication) and certificate rejection wait 30 s. Host key rejection, denied dial-side forwarding and incompatible SSH/TLS negotiation enter `failed`. Correct the cause and call `start()`. Logs contain endpoints and error codes such as `ssh-host-key-rejected` and `ssh-forward-denied`. Changing only the retry setting preserves a healthy connection.

## Verification and other modes

Run the following from the repository root. Host tests need Node 22.18+, OpenSSL, the OpenSSH tools `ssh-keygen`, `ssh-agent`, `ssh-add` and a locally runnable `sshd` (override its path with `SSHD`). They generate temporary keys and use loopback endpoints.

```sh
go -C link/ssh test -race ./...
npm ci --prefix link/tcp
node --test link/ssh/test/*.mjs
```

Existing external OpenSSH reverse forwards can carry the TCP plugin. Starting a rutis endpoint on the VPS through an SSH command would require a dedicated stdio command or subsystem in a future mode. [Reverse forwarding guide](../guide/link-plugins.en.md)
