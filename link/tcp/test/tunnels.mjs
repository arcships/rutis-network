// Run with npm run test:tunnels. All subprocesses and endpoints are local.
import test from 'node:test'
import { randomBytes, randomUUID } from 'node:crypto'
import { once } from 'node:events'
import { readFile, writeFile } from 'node:fs/promises'
import { createServer } from 'node:net'
import { userInfo } from 'node:os'
import { join } from 'node:path'
import { exec, processFor, project, row, setup, startHost } from './fixtures/hosts.mjs'

async function availablePort() {
  const reservation = createServer()
  reservation.listen(0, '127.0.0.1')
  await once(reservation, 'listening')
  const port = reservation.address().port
  await new Promise(resolve => reservation.close(resolve))
  return port
}

async function frp(env, listenerPort) {
  const controlPort = await availablePort()
  const forwardPort = await availablePort()
  const tokenFile = join(env.dir, 'frp.token')
  await writeFile(tokenFile, randomBytes(32).toString('hex'), { mode: 0o600 })
  const options = { env: {
    ...process.env, FRP_CONTROL_PORT: String(controlPort), LINK_FORWARD_PORT: String(forwardPort),
    LINK_LISTEN_PORT: String(listenerPort), FRP_TOKEN_FILE: tokenFile,
    FRP_CA_FILE: env.identity.ca, FRP_CERT_FILE: env.identity.cert, FRP_KEY_FILE: env.identity.key,
  } }
  const server = processFor(env, 'frps', ['-c', join(project, 'tunnels/frps.toml')], options)
  await server.waitFor(/frps started successfully/)
  const client = processFor(env, 'frpc', ['-c', join(project, 'tunnels/frpc.toml')], options)
  await client.waitFor(/start proxy success/)
  return forwardPort
}

async function ssh(env, listenerPort) {
  const port = await availablePort()
  const hostKey = join(env.dir, 'ssh-host')
  const identity = join(env.dir, 'ssh-client')
  const sshdConfig = join(env.dir, 'sshd_config')
  const sshConfig = join(env.dir, 'ssh_config')
  const knownHosts = join(env.dir, 'known_hosts')
  for (const key of [hostKey, identity]) {
    await exec('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', key])
  }
  await writeFile(knownHosts, `[127.0.0.1]:${port} ` + await readFile(hostKey + '.pub', 'utf8'))
  await writeFile(sshdConfig, [
    'ListenAddress 127.0.0.1', `Port ${port}`, `HostKey "${hostKey}"`,
    `PidFile "${env.dir}/sshd.pid"`, `AuthorizedKeysFile "${identity}.pub"`,
    `AllowUsers ${userInfo().username}`, 'AuthenticationMethods publickey', 'PasswordAuthentication no',
    'KbdInteractiveAuthentication no', 'AllowTcpForwarding remote', 'PermitListen 127.0.0.1:*',
    'GatewayPorts no', 'MaxSessions 0',
  ].join('\n') + '\n')
  await writeFile(sshConfig, [
    'Host fixture', 'HostName 127.0.0.1', `Port ${port}`, `User ${userInfo().username}`,
    `IdentityFile "${identity}"`, 'IdentitiesOnly yes', `UserKnownHostsFile "${knownHosts}"`,
  ].join('\n') + '\n')
  const daemon = processFor(env, process.env.SSHD ?? '/usr/sbin/sshd', ['-D', '-e', '-f', sshdConfig])
  await daemon.waitFor(/Server listening on 127\.0\.0\.1/)
  const forward = processFor(env, join(project, 'tunnels/ssh-reverse.sh'), [sshConfig, 'fixture', '0', String(listenerPort)])
  return Number((await forward.waitFor(/Allocated port (\d+) for remote forward/))[1])
}

for (const [name, forward] of [['frp', frp], ['OpenSSH', ssh]]) {
  test(`real rutis call through a ${name} reverse tunnel`, async t => {
    const env = await setup(t)
    const proof = randomUUID()
    const receiver = await startHost(env, 'harbor', [
      row('accept', 'go:link-tcp-listen', { ...env.identity, port: 0 }),
      row('beacon', 'test/fixtures/witness.mjs', { proof }),
    ])
    const listenerPort = Number((await receiver.waitFor(/link\/tcp listening 127\.0\.0\.1:(\d+)/))[1])
    const port = await forward(env, listenerPort)
    const sender = await startHost(env, 'coast', [row('dial', 'go:link-tcp-dial', { ...env.identity, port })])
    await sender.call(proof)
  })
}
