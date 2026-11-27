// Uses the stock rutis host, two Go transports and a Node management caller.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile, writeFile } from 'node:fs/promises'
import { userInfo } from 'node:os'
import { join } from 'node:path'
import { exec, processFor, row, setup, startHost } from '../../tcp/node/test/fixtures/hosts.mjs'
import { manager } from '../../tcp/node/test/fixtures/manager.mjs'
import { availablePort, sshServer } from '../../tcp/node/test/fixtures/sshd.mjs'

const project = new URL('../', import.meta.url)

async function listener(env, name) {
  const service = name + '-control'
  const isolate = { [service]: 'management' }
  const control = manager(env, name, service, isolate)
  const host = await startHost(env, name, [
    { ...row('accept', 'go:link-tcp-listen', { ...env.identity, controlService: service, port: 0 }), isolate },
    row('beacon', 'test/fixtures/witness.mjs', { proof: name }), control.ui,
  ])
  await host.waitFor(/management socket ready/)
  const { boundPort: port } = await control.until(s => s.phase === 'listening')
  return { ...control, host, port, address: '127.0.0.1:' + port }
}

test('managed direct-tcpip admits, delivers calls, survives manager reload and switches targets', { timeout: 60000 }, async t => {
  const env = await setup(t)
  await exec('go', ['build', '-o', join(env.goDir, 'link-ssh'), './cmd/link-ssh'], { cwd: project })
  const first = await listener(env, 'harbor')
  const second = await listener(env, 'island')
  const ssh = await sshServer(env, ['AllowTcpForwarding local', 'PermitOpen ' + first.address + ' ' + second.address])
  const wrongHosts = join(env.dir, 'unknown-host')
  await writeFile(wrongHosts, `[127.0.0.1]:${ssh.port} ` + await readFile(ssh.identity + '.pub', 'utf8'))
  const key = { ...env.identity, user: userInfo().username, knownHosts: ssh.knownHosts, identityFile: ssh.identity }
  const profiles = { key, wrongHost: { ...key, knownHosts: wrongHosts }, wrongUserKey: { ...key, identityFile: ssh.hostKey } }
  const service = 'ssh-control'
  const isolate = { [service]: 'local-management' }
  const { ui, invoke, until } = manager(env, 'coast', service, isolate)
  const transport = { ...row('wire', 'go:link-ssh-dial', { controlService: service, profiles }), isolate }
  const sender = await startHost(env, 'coast', [transport, ui])
  await sender.waitFor(/management socket ready/)
  const initial = await invoke('status')
  assert.equal(initial.phase, 'awaiting-config')
  const change = invoke('waitForChange', initial.cursor)
  const sshAddress = '127.0.0.1:' + ssh.port
  await invoke('configure', { sshAddress, targetAddress: first.address, profile: 'wrongHost' })
  assert.notEqual((await change).cursor, initial.cursor)
  const failed = await until(s => s.phase === 'failed')
  assert.equal(failed.lastError, 'ssh-host-key-rejected')
  assert.equal(failed.retryCount, 0)
  await invoke('configure', { profile: 'wrongUserKey', reconnect: false })
  await until(s => s.lastError === 'ssh-handshake-failed')
  await invoke('configure', { profile: 'key', servername: 'wrong.example' })
  await until(s => s.lastError === 'certificate-rejected')
  await invoke('configure', { servername: 'localhost', targetAddress: '127.0.0.1:' + await availablePort() })
  await until(s => s.lastError === 'ssh-forward-denied')
  assert.equal((await first.invoke('status')).connections, 0)
  await invoke('configure', { targetAddress: first.address })
  await sender.call('harbor')
  const connected = await until(s => s.phase === 'connected')
  assert.equal(connected.remoteAddress, sshAddress)
  assert.equal(JSON.stringify(connected).includes(env.dir), false)
  await assert.rejects(invoke('configure', { targetAddress: second.address, profile: 'undeclared' }))
  await invoke('configure', { sshAddress })
  await invoke('start')
  assert.deepEqual(await invoke('status'), connected)
  await assert.rejects(first.host.rootCall(service + '.status'), /no row provides/)
  await sender.rootCall('rutis/loader.remove', [ui.id])
  await sender.call('harbor', 38)
  const offset = sender.output().length
  await sender.reload([transport, ui])
  await sender.waitFor(/management socket ready/, offset)
  assert.deepEqual(await invoke('status'), connected)
  await first.host.rootCall('rutis/loader.remove', ['beacon'])
  await first.host.rootCall('rutis/loader.create', [row('beacon', 'test/fixtures/witness.mjs', { proof: 'restarted' })])
  await sender.call('restarted', 39)
  assert.deepEqual(await invoke('status'), connected)
  await invoke('configure', { reconnect: true })
  assert.equal((await invoke('status')).localAddress, connected.localAddress)

  // Lose the remote listener, observe backoff, then recover without another command.
  await first.invoke('stop')
  const retrying = await until(s => s.phase === 'retrying')
  assert.ok(retrying.retryCount >= 1 && Date.parse(retrying.retryAt))
  await invoke('configure', { reconnect: false })
  assert.equal((await invoke('status')).phase, 'failed')
  await invoke('configure', { reconnect: true })
  await until(s => s.phase === 'retrying')
  await first.invoke('configure', { port: first.port })
  await first.invoke('start')
  await sender.call('restarted', 40)

  // A target change retires the old session; stop leaves the management service available.
  await invoke('configure', { targetAddress: second.address })
  await sender.call('island')
  await invoke('stop')
  await invoke('configure', { targetAddress: first.address })
  assert.equal((await invoke('status')).phase, 'stopped')
  await invoke('start')
  await sender.call('restarted', 41)
  await sender.rootCall('rutis/loader.remove', ['wire'])
  await first.until(s => s.connections === 0)
})

test('a static row uses a local SSH agent for a remote RPC', { timeout: 30000 }, async t => {
  const env = await setup(t)
  await exec('go', ['build', '-o', join(env.goDir, 'link-ssh'), './cmd/link-ssh'], { cwd: project })
  const target = await listener(env, 'harbor')
  const ssh = await sshServer(env, ['AllowTcpForwarding local', 'PermitOpen ' + target.address])
  const socket = join(env.dir, 'agent.sock')
  const agent = processFor(env, 'ssh-agent', ['-D', '-a', socket])
  await agent.waitFor(/SSH_AUTH_SOCK=/)
  const options = { env: { ...process.env, SSH_AUTH_SOCK: socket } }
  await exec('ssh-add', [ssh.identity], options)
  const sender = await startHost(env, 'coast', [row('wire', 'go:link-ssh-dial', {
    sshAddress: '127.0.0.1:' + ssh.port, targetAddress: target.address, profile: 'agent',
    profiles: { agent: { ...env.identity, user: userInfo().username, knownHosts: ssh.knownHosts } },
  })], options)
  await sender.call('harbor')
})
