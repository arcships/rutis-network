# `link/ssh`: design

Status: draft. Date: 2026-11-08. Baseline: rutis 0.21.0.
Issue: [#3](https://github.com/arcships/rutis-network/issues/3). Depends on [#1](https://github.com/arcships/rutis-network/issues/1), [#6](https://github.com/arcships/rutis-network/issues/6).

## 1. Goal

Let a user who already has SSH connect another machine to rutis without a VPN or a public port. SSH owns admission; rutis adds no token.

## 2. Modes

### 2.1 stdio (first reference implementation)

```text
this end link/ssh ── ssh host -- <rutis runtime/bridge> ── far end
                     stdin/stdout as a byte stream
```

- Start `ssh host -- <command>`; treat stdin/stdout as a duplex byte stream. Framing is chosen by the remote command: a `pipe::framed`-compatible rutis runtime/bridge uses newline framing; a #6 bridge uses the 4-byte length prefix. Then call `rutis/sessions.open(far)`.
- The remote command must speak the session protocol: a rutis runtime, a bridge with `rutis-bridge/session`, or another rutis.
- Advantage: no extra `sshd` port, no `-L`/`-R`; host key, agent and `authorized_keys` are reused.
- The session handshake itself is 10 s (`Session::handshake`); the 30 s greeting limit of `Process::mount` / `rutis-host/spawn` only applies when this end starts a runtime process, not to SSH stdio. An SSH process that never greets must fail and end the process, not wait forever.

### 2.2 forward

```text
this end link/tcp ── 127.0.0.1:<local> ── ssh -L/-R ── far TCP
```

- Use `ssh -L` / `ssh -R` to bring a far port local (or reverse), then hand it to #6's `link/tcp`.
- Use when the far end already listens for rutis over WebSocket/TCP; link/ssh only carries the port.
- Do not repeat framing, close or backpressure in link/ssh.

### 2.3 Library mode (optional)

- Rust `russh`/`ssh2`, Go `x/crypto/ssh`, Python `asyncssh`, Node `ssh2`, Swift `swift-nio-ssh` open a `direct-tcpip` channel.
- Pro: no child process, connection reuse. Con: one SSH library per language, and auth/known_hosts behaviour must be aligned.
- Not first; stdio and forward cover most cases.

## 3. Admission

| Item | Rule |
| --- | --- |
| host key | System `known_hosts`; do not skip verification; first connection follows OpenSSH |
| user auth | SSH agent / key / `authorized_keys`; no rutis token |
| remote account | Decided by SSH; not mapped to session identity |
| failure | Report the SSH exit/error; do not call `open`; create no session |

Do not pass the SSH user, host-key fingerprint or agent identity into the session or `<id>:<plugin>` naming.

## 4. Framing

stdio is a byte stream, so frame it; the remote command decides the rule:

- A `pipe::framed`-compatible rutis runtime/bridge uses newline framing, max 16 MiB. The session JSON escapes newlines inside strings and emits none between tokens, so newline framing is safe and matches the core `pipe::lines`.
- A #6 bridge uses a 4-byte big-endian length + payload, max 16 MiB; reject an over-limit length before allocating.
- Do not mix the two; the SSH command configuration must say which one the far end uses.
- Empty messages (length 0) are allowed.
- EOF: `near.end({kind:'done'})`. Write failure: close and let the session unload.

## 5. Lifecycle and reconnect

- SSH process exits / channel closes → `near.end`; session unloads → close the SSH process/channel.
- Reconnect lives in link/ssh: exponential backoff (0.5 s → 30 s); an auth rejection retries every 30 s; a protocol mismatch stops; the backoff resets after 60 s connected.
- 0.20 unload order: register the child kill/wait once; a cleanup does not register or wait for another cleanup.
- 0.21 async `send`: SSH stdin write blocking applies backpressure through async `send`; do not queue without bound.
- Reap the child to avoid zombies; SSH stderr belongs in link-plugin errors, not in the session byte stream.

## 6. Listen side

- Rust uses `Session::handshake` → `Greeted::id()` to replace a same-id session.
- Non-Rust `open(far)` does not expose the far id, and `session_key(row)` is first-wins, so non-Rust listeners do not support same-id replacement and must be Rust (O1). Do not write "coexist".
- Non-Rust also cannot get structured handshake errors before O1 (handshake result API), so it cannot reliably tell an auth rejection from a protocol mismatch; do not treat an adapter close reason as a handshake result, fall back to transport backoff and record the limitation.
- Do not parse `Hello`; do not substitute the SSH user/hostname for the id.

## 7. Errors

| Case | Handling |
| --- | --- |
| SSH connect/auth failure | Report host, port and the SSH error; do not `open` |
| Remote command missing | Report the command and exit code |
| Far end never greets | Fail within 10 s with a session handshake timeout (`the far end did not greet in time`) and end the SSH process |
| Frame over limit | Close, report the limit |
| SSH disconnect | `near.end({kind:'done'})`; reconnect with backoff |
| Protocol mismatch | Report the version; do not retry |

0.21 error texts are not a contract; link-plugin errors use codes/structured fields, while the SSH error itself is kept verbatim.

## 8. Security boundary

- SSH is the admission boundary; rutis trusts connections SSH lets in.
- Never log private keys, the agent socket or passwords.
- The remote command is configuration, not session data; do not build it from messages.
- Document Windows OpenSSH stdio/signal behaviour separately.

## 9. Tests

- Local sshd or fake channel: stdio duplex, boundaries/order, 16 MiB limit, close/EOF; both framings (newline, 4-byte) once each.
- No greeting: 10 s session handshake timeout and process end; the 30 s `Process::mount` limit only applies to local spawn.
- Auth failure: no session.
- `ssh -L` / `-R` + `link/tcp`: the same session works.
- Disconnect: backoff, auth rejection every 30 s, stop on protocol mismatch; reset after 60 s connected.
- Platform matrix: Linux + macOS required; Windows OpenSSH manual or CI if available, with signal/process differences recorded.
- No public SSH service in CI.

## 10. Non-goals

- No SSH server.
- No SSH CA/certificate issuance.
- No mapping SSH auth to a rutis token.
- No SSH as a core dependency.

## 11. Open questions

- O1: What exactly runs at the far end in stdio mode, and with which arguments does a rutis runtime speak the session protocol over stdin/stdout?
- O2: Reuse `rutis-host/spawn`'s `pipe::spawn` semantics (socket/loopback), or let SSH create the pipe?
- O3: Is library mode worth it for mobile/embedded?

## Links

- Issue [#3](https://github.com/arcships/rutis-network/issues/3)
- [Contract design](01-link-plugin-contract.en.md)
- [TCP bridge design](06-link-tcp.en.md)
- rutis `docs/migration-0.20.md` (runtime greeting limit)
- rutis `crates/rutis-bridge/src/pipe/mod.rs`
