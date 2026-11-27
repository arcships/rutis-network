import test from 'node:test'
import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { once } from 'node:events'
import { writeFile } from 'node:fs/promises'
import { createConnection, createServer } from 'node:net'
import { setTimeout as delay } from 'node:timers/promises'
import { join } from 'node:path'
import { rootCertificates } from 'node:tls'
import dial from '../../dial.ts'
import { row, setup, startHost } from './fixtures/hosts.mjs'

test('Go and TypeScript clients, mTLS admission, chunked echo, row unload and reconnection', async (t) => {
  const env = await setup(t)
  const proof = randomUUID()
  const witness = row('beacon', 'test/fixtures/witness.mjs', { proof })
  const listen = (port) => row('accept', 'go:link-tcp-listen', { ...env.identity, port })
  const receiver = await startHost(env, 'harbor', [listen(0), witness])
  const listening = /link\/tcp listening 127\.0\.0\.1:(\d+)/
  const port = Number((await receiver.waitFor(listening))[1])
  const config = { ...env.identity, port }
  const wrongCa = join(env.dir, 'other-ca.pem')
  await writeFile(wrongCa, rootCertificates[0])
  let opened = 0
  const ctx = {
    signal: new AbortController().signal,
    effect() {},
    use() {
      return {
        open() {
          opened++
        },
      }
    },
  }
  await assert.rejects(dial.apply(ctx, { ...config, ca: wrongCa }), (error) =>
    ['DEPTH_ZERO_SELF_SIGNED_CERT', 'SELF_SIGNED_CERT_IN_CHAIN'].includes(error.code),
  )
  assert.equal(opened, 0)
  const denied = await startHost(env, 'denied', [row('dial', 'go:link-tcp-dial', { ...config, ca: wrongCa })])
  await denied.waitFor(/certificate-rejected/)
  const coast = await startHost(env, 'coast', [
    row('dial', 'go:link-tcp-dial', { ...config, reconnect: true }),
  ])
  const island = await startHost(env, 'island', [
    row('dial', 'go:link-tcp-dial', config),
    row('probe', 'test/fixtures/large.mjs', {}),
  ])
  const reference = await startHost(env, 'reference', [row('dial', '../dial.ts', config)])
  await Promise.all([coast.call(proof), island.call(proof), reference.call(proof)])

  // The witness is reachable. Run the large transfer once, including under
  // the Go race detector, which instruments every byte crossing the SDK.
  const bytes = 17 * 1024 * 1024
  const transfer = await island.rootCall('probe.roundTripChunks', [bytes], 180000)
  assert.deepEqual(JSON.parse(transfer.stdout), [proof, bytes])
  // This row dials once. A subsequent call must travel over the same connection.
  await island.call(proof, 73)
  await coast.call(proof)
  await reference.call(proof)

  const stalled = createConnection(port, '127.0.0.1')
  t.after(() => stalled.destroy())
  await once(stalled, 'connect')
  const closed = once(stalled, 'close')
  await receiver.reload([witness])
  await closed
  await coast.waitFor(/retry in 500ms/)
  const offset = receiver.output().length
  const replacement = randomUUID()
  await receiver.reload([listen(port), row('next', 'test/fixtures/witness.mjs', { proof: replacement })])
  await receiver.waitFor(listening, offset)
  await coast.call(replacement, 73)

  // Unload the reconnecting row before another disconnect; no new dial should occur.
  const beforeUnload = coast.output().length
  await coast.reload([])
  await coast.waitFor(/dial: (?:removed|unloaded|stopped)/, beforeUnload)
})

test('unloading during initial backoff cancels the next TCP connection', { timeout: 10000 }, async (t) => {
  const env = await setup(t)
  let attempts = 0
  const server = createServer((socket) => {
    attempts++
    socket.destroy()
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  t.after(() => server.close())
  const host = await startHost(env, 'coast', [
    row('dial', 'go:link-tcp-dial', { ...env.identity, port: server.address().port, reconnect: true }),
  ])
  await host.waitFor(/retry in 500ms/)
  await host.reload([])
  await host.waitFor(/dial: (?:removed|unloaded|stopped)/)
  const afterUnload = attempts
  await delay(1100)
  assert.ok(afterUnload >= 1)
  assert.equal(attempts, afterUnload)
})
