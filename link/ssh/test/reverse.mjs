// Both rutis hosts dial a stock loopback sshd. Only the receiver requests a remote bind.
import test from 'node:test'
import assert from 'node:assert/strict'
import { once } from 'node:events'
import { createConnection, createServer } from 'node:net'
import { connect as tlsConnect } from 'node:tls'
import { readFile, writeFile } from 'node:fs/promises'
import { userInfo } from 'node:os'
import { join } from 'node:path'
import { exec, row, setup, startHost } from '../../tcp/node/test/fixtures/hosts.mjs'
import { manager } from '../../tcp/node/test/fixtures/manager.mjs'
import { availablePort, sshServer } from '../../tcp/node/test/fixtures/sshd.mjs'

async function bind(env, server) {
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  env.stops.push(() => new Promise(resolve => server.close(resolve)))
  return server.address().port
}

// Interrupt only A's outer SSH socket while leaving sshd and B running.
async function relay(env) {
  const ssh = await sshServer(env, ['AllowTcpForwarding yes', 'PermitListen 127.0.0.1:*', 'PermitOpen 127.0.0.1:*'])
  const sockets = new Set()
  let available = true
  const proxy = createServer(socket => {
    if (!available) return socket.destroy()
    const upstream = createConnection(ssh.port, '127.0.0.1')
    for (const [a, b] of [[socket, upstream], [upstream, socket]]) {
      sockets.add(a)
      a.on('error', () => b.destroy())
      a.on('close', () => { sockets.delete(a); b.destroy() })
      a.pipe(b)
    }
  })
  const port = await bind(env, proxy)
  const disconnect = () => { available = false; for (const socket of sockets) socket.destroy() }
  env.stops.push(disconnect)
  const knownHosts = join(env.dir, 'relay-hosts')
  await writeFile(knownHosts, await readFile(ssh.knownHosts, 'utf8') +
    `[127.0.0.1]:${port} ` + await readFile(ssh.hostKey + '.pub', 'utf8'))
  const profile = { user: userInfo().username, knownHosts, identityFile: ssh.identity }
  return { profile, proxy: '127.0.0.1:' + port, direct: '127.0.0.1:' + ssh.port,
    disconnect, resume() { available = true } }
}

for (const mode of ['listen', 'tunnel']) {
  test(`SSH ${mode} accepts a rutis peer through a VPS, rebinds after SSH loss and releases its listener`, { timeout: 60000 }, async t => {
    const env = await setup(t)
    await exec('go', ['build', '-o', join(env.goDir, 'link-ssh'), './cmd/link-ssh'], { cwd: new URL('../', import.meta.url) })
    const gateway = await relay(env)
    const key = { ...gateway.profile, ...env.identity }
    const witness = row('beacon', 'test/fixtures/witness.mjs', { proof: mode })
    const service = 'reverse-control'
    const isolate = { [service]: 'management' }
    const ui = manager(env, 'office', service, isolate)
    const config = { sshAddress: gateway.proxy, profile: 'key', controlService: service,
      profiles: { key: mode === 'listen' ? key : gateway.profile } }
    if (mode === 'tunnel') {
      const origin = await startHost(env, 'origin', [witness, row('tcp', 'go:link-tcp-listen', { port: 0, ...env.identity })])
      const [, port] = await origin.waitFor(/link\/tcp listening 127\.0\.0\.1:(\d+)/)
      config.localTarget = '127.0.0.1:' + port
    }
    const receiver = { ...row('wire', 'go:link-ssh-' + mode, config), isolate }
    const rows = [receiver, ui.ui]
    if (mode === 'listen') rows.push(witness)
    const office = await startHost(env, 'office', rows)
    await office.waitFor(/management socket ready/)
    assert.equal((await ui.invoke('status')).phase, 'awaiting-config')
    await ui.invoke('configure', { listenAddress: '127.0.0.1:0' })
    const dynamic = await ui.until(s => s.phase === 'listening')
    assert.match(dynamic.boundAddress, /^127\.0\.0\.1:[1-9]\d*$/)
    assert.equal(dynamic.connections, 0)
    await assert.rejects(ui.invoke('configure', { listenAddress: '0.0.0.0:17443' }))
    assert.deepEqual(await ui.invoke('status'), dynamic)

    if (mode === 'listen') {
      // TLS without a client certificate must never mount a rutis session.
      const socket = tlsConnect({ port: Number(dynamic.boundAddress.split(':')[1]), host: '127.0.0.1',
        ca: await readFile(env.identity.ca) })
      socket.on('error', () => {})
      await new Promise(resolve => socket.once('close', resolve))
      const rejected = await ui.until(s => Boolean(s.lastClientError))
      assert.equal(rejected.phase, 'listening')
      assert.equal(rejected.connections, 0)
    }

    // OpenSSH returns the same failure for a busy port and a rejected bind.
    const occupied = createServer(socket => socket.destroy())
    const busy = await bind(env, occupied)
    await ui.invoke('configure', { listenAddress: '127.0.0.1:' + busy, reconnect: false })
    const denied = await ui.until(s => s.phase === 'failed')
    assert.equal(denied.lastError, 'ssh-listen-failed')
    assert.equal(denied.boundAddress, undefined)

    const address = '127.0.0.1:' + await availablePort()
    await ui.invoke('configure', { listenAddress: address, reconnect: true })
    await ui.until(s => s.boundAddress === address)
    const dial = row('wire', 'go:link-ssh-dial', { sshAddress: gateway.direct, targetAddress: address,
      profile: 'key', profiles: { key }, reconnect: true })
    const peer = await startHost(env, 'coast', [dial])
    await peer.call(mode)
    await ui.until(s => s.connections === 1)
    if (mode === 'listen') {
      const other = await startHost(env, 'island', [dial])
      await other.call(mode)
      await ui.until(s => s.connections === 2)
      await other.rootCall('rutis/loader.remove', ['wire'])
      await ui.until(s => s.connections === 1)
      await peer.call(mode, 37)
    }
    gateway.disconnect()
    const lost = await ui.until(s => s.phase === 'retrying')
    assert.equal(lost.connections, 0)
    assert.equal(lost.boundAddress, undefined)
    gateway.resume()
    await ui.until(s => s.boundAddress === address)
    await peer.call(mode, 38)
    await ui.until(s => s.connections === 1)

    if (mode === 'tunnel') {
      // A plain TCP origin can wait for request EOF before sending its full reply.
      const echo = createServer({ allowHalfOpen: true }, socket => {
        const request = []
        socket.on('data', chunk => request.push(chunk))
        socket.on('end', () => socket.end(Buffer.concat(request)))
      })
      const port = await bind(env, echo)
      await peer.rootCall('rutis/loader.remove', ['wire'])
      await ui.invoke('configure', { localTarget: '127.0.0.1:' + port })
      await ui.until(s => s.boundAddress === address)
      const socket = createConnection(Number(address.split(':')[1]), '127.0.0.1')
      const message = Buffer.alloc(128 * 1024, 173)
      socket.end(message)
      const reply = []
      for await (const data of socket) reply.push(data)
      assert.deepEqual(Buffer.concat(reply), message)
    }

    await ui.invoke('stop')
    const stopped = await ui.invoke('status')
    assert.equal(stopped.phase, 'stopped')
    assert.equal(stopped.connections, 0)
    assert.equal(stopped.boundAddress, undefined)
    await ui.invoke('start')
    await ui.until(s => s.boundAddress === address)
    await office.rootCall('rutis/loader.remove', ['wire'])
    const [error] = await once(createConnection(Number(address.split(':')[1]), '127.0.0.1'), 'error')
    assert.equal(error.code, 'ECONNREFUSED')
  })
}

test('a single host runs both static reverse roles with independent lifetimes', { timeout: 45000 }, async t => {
  const env = await setup(t)
  await exec('go', ['build', '-o', join(env.goDir, 'link-ssh'), './cmd/link-ssh'], { cwd: new URL('../', import.meta.url) })
  const gateway = await relay(env)
  const linkKey = { ...gateway.profile, ...env.identity }
  const options = { sshAddress: gateway.direct, profile: 'key', reconnect: false }
  const origin = await startHost(env, 'origin', [
    row('tcp', 'go:link-tcp-listen', { ...env.identity, port: 0 }),
    row('origin-beacon', 'test/fixtures/witness.mjs', { proof: 'forwarded' }),
  ])
  const [, port] = await origin.waitFor(/link\/tcp listening 127\.0\.0\.1:(\d+)/)
  const listenAddress = '127.0.0.1:0'
  const listening = /link\/ssh listening (127\.0\.0\.1:\d+)\s/
  const wire = row('accept', 'go:link-ssh-listen', { ...options, profiles: { key: linkKey }, listenAddress })
  const office = await startHost(env, 'office', [wire, row('beacon', 'test/fixtures/witness.mjs', { proof: 'mounted' })])
  const [, directAddress] = await office.waitFor(listening)
  // The listener binds during apply, before the host finishes installing its file rows.
  await Promise.all([office.waitFor(/accept: running/), office.waitFor(/beacon: running/)])
  const logOffset = office.output().length
  const forward = row('forward', 'go:link-ssh-tunnel', { ...options, profiles: { key: gateway.profile },
    listenAddress, localTarget: '127.0.0.1:' + port })
  await office.rootCall('rutis/loader.create', [forward])
  const [, forwardAddress] = await office.waitFor(listening, logOffset)
  assert.notEqual(forwardAddress, directAddress)
  const dial = targetAddress => row('dial', 'go:link-ssh-dial', { ...options, targetAddress, profiles: { key: linkKey } })
  const directPeer = await startHost(env, 'direct', [dial(directAddress)])
  const forwardPeer = await startHost(env, 'forwarded', [dial(forwardAddress)])
  await directPeer.call('mounted')
  await forwardPeer.call('forwarded')
  await office.rootCall('rutis/loader.remove', ['accept'])
  await forwardPeer.call('forwarded', 41)
})
