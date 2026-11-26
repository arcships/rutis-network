import test from 'node:test'
import assert from 'node:assert/strict'
import { request } from 'node:http'
import { createServer } from 'node:net'
import { once } from 'node:events'
import { join } from 'node:path'
import { writeFile } from 'node:fs/promises'
import { setTimeout as delay } from 'node:timers/promises'
import { exec, project, row, setup, startHost } from './fixtures/hosts.mjs'

function instance(env, id, mode, config = {}) {
  const service = id + '-control'
  const isolate = { [service]: 'management' }
  const socket = join(env.dir, id + '.sock')
  const transport = { ...row(id + '-wire', 'go:link-tcp-' + mode, {
    ...env.identity, controlService: service, ...config,
  }), isolate }
  const ui = { ...row(id + '-ui', 'test/fixtures/controller.mjs', { service, socket }),
    inject: [service], isolate }
  function invoke(method, ...args) {
    return new Promise((resolve, reject) => {
      const req = request({ socketPath: socket, method: 'POST' }, async res => {
        try {
          const chunks = []
          for await (const chunk of res) chunks.push(chunk)
          const text = Buffer.concat(chunks).toString()
          if (res.statusCode !== 200) throw new Error(text)
          resolve(JSON.parse(text))
        } catch (error) { reject(error) }
      })
      req.on('error', reject)
      req.end(JSON.stringify([method, ...args]))
    })
  }
  async function until(check) {
    for (let i = 0; i < 150; i++) {
      const s = await invoke('status')
      if (check(s)) return s
      await delay(25)
    }
    assert.fail('unexpected ' + id + ' state: ' + JSON.stringify(await invoke('status')))
  }
  return { invoke, until, service, rows: [transport, ui] }
}

async function managed(env, id, mode, config, otherRows = []) {
  const link = instance(env, id, mode, config)
  const rows = [...link.rows, ...otherRows]
  const host = await startHost(env, id, rows)
  await host.waitFor(/management socket ready/)
  return { ...host, ...link, rows }
}

test('a TCP row mounted by the loader inherits the group’s local control scope', { timeout: 25000 }, async t => {
  const env = await setup(t)
  const harbor = await managed(env, 'harbor', 'listen', { port: 0 }, [
    row('beacon', 'test/fixtures/witness.mjs', { proof: 'harbor' }),
  ])
  const { boundPort } = await harbor.until(s => s.phase === 'listening')
  const link = instance(env, 'coast', 'dial')
  const [wire, caller] = link.rows
  const group = { id: 'local-links', group: true, isolate: wire.isolate, config: [] }
  const coast = await startHost(env, 'coast', [group, caller])
  const options = { id: wire.id, name: wire.name, config: wire.config }
  await coast.waitFor(/running|waiting/)
  const result = await coast.rootCall('rutis/loader.create', [options, group.id])
  assert.equal(JSON.parse(result.stdout), wire.id)
  await coast.waitFor(/management socket ready/)
  await link.invoke('configure', { port: boundPort })
  await coast.call('harbor')
  await harbor.until(s => s.connections === 1)
  await assert.rejects(harbor.rootCall(link.service + '.status'), /no row provides coast-control/)
  await coast.rootCall('rutis/loader.remove', [wire.id])
  await harbor.until(s => s.connections === 0)
})

test('a dynamic TCP row survives manager and business plugin restarts until the transport is removed', { timeout: 45000 }, async t => {
  const env = await setup(t)
  await exec('go', ['build', '-o', join(env.goDir, 'mounter'), './test/fixtures/mounter'], { cwd: project })
  const isolate = { 'office-tcp': 'management', 'office-owner': 'management' }
  const owner = row('owner', 'go:mounter', {})
  const binding = { ...row('deliver', 'go:link-tcp-attach', {
    controlService: 'office-tcp', controlReceiver: 'office-owner',
  }), inject: ['office-tcp', 'office-owner'] }
  const beacon = proof => row('beacon', 'test/fixtures/witness.mjs', { proof })
  const coast = await startHost(env, 'coast', [
    { id: 'local-links', group: true, isolate, config: [binding, owner] }, beacon('first'),
  ])
  await coast.waitFor(/owner: running/)
  const config = { ...env.identity, controlService: 'office-tcp', autoStart: false }
  const invoke = async (method, ...args) => {
    const { stdout } = await coast.rootCall('test-driver.invoke', [method, args])
    return stdout.trim() ? JSON.parse(stdout) : null
  }
  const mount = () => coast.rootCall('test-driver.mount', [config])
  const remove = () => coast.rootCall('test-driver.remove')
  assert.equal(JSON.parse((await mount()).stdout), 'wire')
  const initial = await invoke('status')
  assert.equal(initial.phase, 'idle')
  await assert.rejects(coast.rootCall('rutis/loader.create', [{
    id: 'obsolete', name: 'go:link-tcp-listen',
    config: { ...env.identity, port: 0, controlReceiver: 'office-owner' },
  }, 'local-links']), /controlReceiver/)
  const changed = invoke('waitForChange', initial.cursor)
  await invoke('configure', { port: 0 })
  assert.equal((await changed).config.port, 0)
  await invoke('start')
  let state = await invoke('status')
  while (state.phase === 'starting') state = await invoke('waitForChange', state.cursor)
  assert.equal(state.phase, 'listening')
  assert.ok(state.boundPort > 0)
  const peer = await managed(env, 'peer', 'dial', { port: state.boundPort, reconnect: false })
  await peer.call('first')
  const connected = await peer.until(s => s.phase === 'connected')
  const listening = await invoke('status')
  assert.equal(listening.connections, 1)

  // During the manager's absence even a single lost socket would take this
  // single-attempt dialer to failed. Its source port and cursor stay fixed.
  await assert.rejects(peer.rootCall('office-tcp.status'), /no row provides office-tcp/)
  await assert.rejects(peer.rootCall('office-owner.attach'), /no row provides office-owner/)
  await coast.rootCall('rutis/loader.remove', ['owner'])
  await peer.call('first', 38)
  await coast.rootCall('rutis/loader.create', [owner, 'local-links'])
  assert.deepEqual(await invoke('status'), listening)

  await coast.rootCall('rutis/loader.remove', ['beacon'])
  await coast.rootCall('rutis/loader.create', [beacon('second')])
  await peer.call('second', 39)
  assert.deepEqual(await peer.invoke('status'), connected)
  assert.deepEqual(await invoke('status'), listening)

  // Unloading the actual TCP row closes the connection and its old handle.
  await remove()
  await peer.until(s => s.phase === 'failed')
  await assert.rejects(invoke('status'))
  await mount()
  const fresh = await invoke('waitForChange', initial.cursor)
  assert.equal(fresh.phase, 'idle')
  assert.notEqual(fresh.cursor, initial.cursor)
  await remove()
})

test('managed controls stay local and usable across stop, reconfigure and reload', { timeout: 45000 }, async t => {
  const env = await setup(t)
  const harbor = await managed(env, 'harbor', 'listen', { port: 0 }, [
    row('beacon', 'test/fixtures/witness.mjs', { proof: 'harbor' }),
  ])
  const bound = await harbor.until(s => s.phase === 'listening')
  assert.match(bound.localAddress, /^127\.0\.0\.1:\d+$/)
  const port = bound.boundPort
  assert.equal(bound.localAddress, '127.0.0.1:' + port)
  assert.equal(bound.role, 'listen')
  const coast = await managed(env, 'coast', 'dial')
  const initial = await coast.invoke('status')
  assert.equal(initial.phase, 'awaiting-config')
  assert.equal(initial.role, 'dial')
  await assert.rejects(coast.invoke('start'), /configure port/)
  const change = coast.invoke('waitForChange', initial.cursor)
  await coast.invoke('configure', { host: 'localhost', port })
  assert.notEqual((await change).cursor, initial.cursor)
  const connected = await coast.until(s => s.phase === 'connected')
  assert.equal(connected.config.host, 'localhost')
  assert.equal(connected.config.port, port)
  assert.ok(connected.config.reconnect)
  await coast.invoke('start')
  assert.equal((await coast.invoke('status')).cursor, connected.cursor)
  await coast.call('harbor')
  await harbor.until(s => s.connections === 1)
  // Both sessions carry regular service calls. Neither remote root can invoke
  // the other side's management service even with its exact name.
  await assert.rejects(harbor.rootCall(coast.service + '.stop'), /no row provides coast-control/)
  await assert.rejects(coast.rootCall(harbor.service + '.status'), /no row provides harbor-control/)
  await assert.rejects(coast.invoke('configure', { port: 99999, reconnect: false }), /invalid port/)
  assert.equal((await coast.invoke('status')).cursor, connected.cursor)
  await coast.invoke('configure', { port, host: 'localhost' })
  assert.equal((await coast.invoke('status')).cursor, connected.cursor)
  await coast.invoke('configure', { reconnect: false })
  assert.equal((await coast.invoke('status')).localAddress, connected.localAddress)
  await coast.call('harbor', 38)

  const second = await managed(env, 'island', 'dial', { port, autoStart: false })
  assert.equal((await second.invoke('status')).phase, 'idle')
  await second.invoke('start')
  await harbor.until(s => s.connections === 2)
  await coast.invoke('stop')
  await harbor.until(s => s.connections === 1)
  await second.call('harbor', 39)
  await coast.invoke('configure', { servername: 'localhost', reconnect: true })
  assert.equal((await coast.invoke('status')).phase, 'stopped')
  await coast.invoke('start')
  await coast.call('harbor', 40)
  await harbor.until(s => s.connections === 2)

  await harbor.invoke('stop')
  assert.equal((await harbor.invoke('status')).connections, 0)
  assert.equal((await harbor.invoke('status')).boundPort, undefined)
  await coast.until(s => s.phase === 'retrying' && s.retryAt && s.lastError)
  await coast.invoke('configure', { reconnect: false })
  assert.equal((await coast.invoke('status')).phase, 'failed')
  await coast.invoke('configure', { reconnect: true })
  await coast.until(s => s.phase === 'retrying')
  // The listener has moved. The dialer switches immediately while in
  // backoff even when the new policy allows only one attempt.
  await harbor.invoke('start')
  const rebound = await harbor.until(s => s.phase === 'listening')
  await coast.invoke('configure', { host: '127.0.0.1', servername: null, port: rebound.boundPort, reconnect: false })
  const moved = await coast.until(s => s.phase === 'connected')
  assert.equal(moved.config.servername, undefined)
  await coast.call('harbor', 41)
  await coast.invoke('stop')
  const stopped = await coast.invoke('status')
  await coast.invoke('stop')
  assert.equal((await coast.invoke('status')).cursor, stopped.cursor)
  await coast.reload([])
  await coast.waitFor(/coast-wire: (?:removed|unloaded|stopped)/)
  const offset = coast.output().length
  await coast.reload(coast.rows)
  await coast.waitFor(/management socket ready/, offset)
  const reloaded = await coast.invoke('waitForChange', initial.cursor)
  assert.equal(reloaded.phase, 'awaiting-config')
  assert.notEqual(reloaded.cursor, initial.cursor)
})

test('changing targets or stopping interrupts an unfinished TLS handshake', { timeout: 25000 }, async t => {
  const env = await setup(t)
  const stalled = createServer()
  stalled.listen(0, '127.0.0.1')
  await once(stalled, 'listening')
  t.after(() => stalled.close())
  const coast = await managed(env, 'coast', 'dial')
  const addr = { port: stalled.address().port }
  const accepted = once(stalled, 'connection')
  await coast.invoke('configure', addr)
  const [socket] = await accepted
  socket.resume()
  await coast.until(s => s.phase === 'handshaking')
  const closed = once(socket, 'close')
  await coast.invoke('stop')
  await closed
  const harbor = await managed(env, 'harbor', 'listen', { port: 0 }, [
    row('beacon', 'test/fixtures/witness.mjs', { proof: 'harbor' }),
  ])
  const port = (await harbor.until(s => s.phase === 'listening')).boundPort
  const again = once(stalled, 'connection')
  await coast.invoke('start')
  const [other] = await again
  other.resume()
  const retired = once(other, 'close')
  await coast.invoke('configure', { port })
  await retired
  await coast.call('harbor')
  assert.equal((await coast.invoke('status')).config.port, port)
})

test('managed listener recovers from a busy port and keeps its bind private', { timeout: 20000 }, async t => {
  const env = await setup(t)
  const occupied = createServer().listen(0, '127.0.0.1')
  await once(occupied, 'listening')
  t.after(() => occupied.close())
  const harbor = await managed(env, 'harbor', 'listen', { port: occupied.address().port })
  assert.equal((await harbor.until(s => s.phase === 'failed')).lastError, 'EADDRINUSE')
  await assert.rejects(harbor.invoke('configure', { host: '0.0.0.0' }), /invalid host/)
  await harbor.invoke('configure', { port: 0 })
  const listening = await harbor.until(s => s.phase === 'listening')
  await assert.rejects(harbor.invoke('configure', { cert: 'invalid' }), /unsupported configuration field/)
  assert.equal((await harbor.invoke('status')).cursor, listening.cursor)
  // Rebinding a healthy listener tears down its admitted clients.
  const coast = await managed(env, 'coast', 'dial', { port: listening.boundPort, reconnect: false })
  await harbor.until(s => s.connections === 1)
  await harbor.invoke('configure', { port: occupied.address().port })
  assert.equal((await harbor.until(s => s.phase === 'failed')).connections, 0)
  await coast.until(s => s.phase === 'failed')
})

test('a live dialer switches peers and corrects a mismatched TLS name during backoff', { timeout: 30000 }, async t => {
  const env = await setup(t)
  async function peer(name) {
    const host = await managed(env, name, 'listen', { port: 0 }, [
      row('beacon', 'test/fixtures/witness.mjs', { proof: name }),
    ])
    const { boundPort } = await host.until(s => s.phase === 'listening')
    return { host, port: boundPort }
  }
  const first = await peer('first')
  const next = await peer('next')
  const coast = await managed(env, 'coast', 'dial', { port: first.port })
  await coast.call('first')
  await coast.invoke('configure', { port: next.port, servername: 'different.invalid' })
  const rejected = await coast.until(s => s.phase === 'retrying' && s.lastError === 'certificate-rejected')
  assert.equal(rejected.config.port, next.port)
  await first.host.until(s => s.connections === 0)
  assert.equal((await next.host.invoke('status')).connections, 0)
  await coast.invoke('configure', { servername: null })
  await coast.call('next')
  await next.host.until(s => s.connections === 1)
})

test('two TCP instances on one host retain independent controls and cursors', { timeout: 15000 }, async t => {
  const env = await setup(t)
  const left = instance(env, 'left', 'listen', { port: 0 })
  const right = instance(env, 'right', 'listen', { port: 0 })
  const host = await startHost(env, 'harbor', [...left.rows, ...right.rows])
  await Promise.all([host.waitFor(/management socket ready left-control/), host.waitFor(/management socket ready right-control/)])
  const a = await left.until(s => s.phase === 'listening')
  const b = await right.until(s => s.phase === 'listening')
  assert.notEqual(a.boundPort, b.boundPort)
  assert.notEqual(a.cursor, b.cursor)
  await left.invoke('stop')
  await left.invoke('configure', { port: b.boundPort })
  assert.equal((await left.invoke('status')).phase, 'stopped')
  assert.deepEqual(await right.invoke('status'), b)
})

test('remounting a TCP row on a new peer moves its remote plugin after old children unload', { timeout: 30000 }, async t => {
  const env = await setup(t)
  async function listener(id) {
    const host = await managed(env, id, 'listen', { port: 0 }, [
      row('beacon', 'test/fixtures/witness.mjs', { proof: id }),
    ])
    return {
      port: (await host.until(s => s.phase === 'listening')).boundPort,
      pid: JSON.parse((await host.rootCall('witness.pid')).stdout),
    }
  }
  const first = await listener('first')
  const second = await listener('second')
  const release = join(env.dir, 'release-consumer')
  const wire = port => row('wire', 'go:link-tcp-dial', {
    ...env.identity, port, controlService: 'office-tcp', reconnect: false,
  })
  const coast = await startHost(env, 'coast', [
    { id: 'local-links', group: true, isolate: { 'office-tcp': 'management' }, config: [wire(first.port)] },
    row('consumer', 'test/fixtures/slow-consumer.mjs', { release }),
    { id: 'remote-app', name: 'wire:' + join(project, 'test/fixtures/remote-marker.mjs') },
  ])
  const marker = async () => JSON.parse((await coast.rootCall('routed-marker.pid')).stdout)
  try {
    await coast.call('first')
    await coast.waitFor(/consumer: running/)
    await coast.waitFor(/remote-app: running/)
    assert.equal(await marker(), first.pid)

    const offset = coast.output().length
    let removed = false
    await Promise.all([
      coast.rootCall('rutis/loader.remove', ['wire']).then(() => { removed = true }),
      (async () => {
        await coast.waitFor(/waiting for consumer cleanup/, offset)
        assert.equal(removed, false)
        await writeFile(release, '')
      })(),
    ])
    await coast.rootCall('rutis/loader.create', [wire(second.port), 'local-links'])
    await coast.call('second')
    let last
    for (let i = 0; i < 50; i++) {
      try {
        const pid = await marker()
        assert.equal(pid, second.pid)
        return
      } catch (err) { last = err }
      await delay(50)
    }
    assert.fail([last, (await coast.rootCall('rutis/loader.status')).stdout, coast.output()].join('\n'))
  } finally {
    await writeFile(release, '')
  }
})
