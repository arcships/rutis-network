// Run with npm run test:tunnels. All subprocesses and endpoints are local.
import test from 'node:test'
import { randomBytes, randomUUID } from 'node:crypto'
import { writeFile } from 'node:fs/promises'
import { userInfo } from 'node:os'
import { join } from 'node:path'
import { availablePort, sshServer } from './fixtures/sshd.mjs'
import { processFor, project, row, setup, startHost } from './fixtures/hosts.mjs'

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
  const { port, identity, knownHosts } = await sshServer(env, ['AllowTcpForwarding remote', 'PermitListen 127.0.0.1:*'])
  const sshConfig = join(env.dir, 'ssh_config')
  await writeFile(sshConfig, [
    'Host fixture', 'HostName 127.0.0.1', `Port ${port}`, `User ${userInfo().username}`,
    `IdentityFile "${identity}"`, 'IdentitiesOnly yes', `UserKnownHostsFile "${knownHosts}"`,
  ].join('\n') + '\n')
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
