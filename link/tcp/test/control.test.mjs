import test from 'node:test'
import assert from 'node:assert/strict'
import { request } from 'node:http'
import { createServer } from 'node:net'
import { once } from 'node:events'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { row, setup, startHost } from './fixtures/hosts.mjs'

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
