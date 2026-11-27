import assert from 'node:assert/strict'
import { test } from 'vitest'
import { setImmediate as tick } from 'node:timers/promises'
import { createRequire } from 'node:module'
import { createPageEndpoint } from '../../src/browser/endpoint.ts'
import { dirname, join } from 'node:path'

// Load the stock peer through Node, outside the browser aliases used for the page.
const require = createRequire(import.meta.url)
const runtime = dirname(require.resolve('@arcships/rutis-runtime/package.json'))
const { Session } = require(join(runtime, 'src/session.mjs'))
const { decode } = require(join(runtime, 'src/codec.mjs'))
const decoder = new TextDecoder()

function connect(t, left, right) {
  let a, b
  const frames = []
  const forward = (from, bytes, to) => {
    frames.push({ from, ...decode(decoder.decode(bytes)) })
    return to.receive(bytes)
  }
  a = left.sessions.open({ send: (bytes) => forward('left', bytes, b), close: () => b.end() })
  b = right.sessions.open({ send: (bytes) => forward('right', bytes, a), close: () => a.end() })
  t.onTestFinished(() => {
    a.end()
    b.end()
  })
  return frames
}

test('a name collision leaves other RPCs running and clears when a provider withdraws', async (t) => {
  const errors = []
  const options = { onServiceError: (issue) => errors.push(issue) }
  const left = createPageEndpoint('left', options),
    right = createPageEndpoint('right', options)
  const remove = left.provide('same', { echo: () => 'left' })
  right.provide('same', { echo: () => 'right' })
  right.provide('other', { echo: (v) => v })
  const frames = connect(t, left, right)
  await tick()
  const greetings = frames.filter((f) => f.op === 'hello')
  assert.equal(greetings.length, 2)
  assert.ok(greetings.every((f) => !f.capabilities.includes('node')))
  assert.equal(errors.length, 2)
  assert.ok(errors.every((e) => e.service === 'same' && e.operation === 'services.announce'))
  assert.deepEqual(left.remoteServices(), ['other'])
  assert.equal(await left.call('other', 'echo', 42), 42)
  remove()
  await tick()
  assert.equal(await left.call('same', 'echo'), 'right')
})

test('concurrent async handlers keep distinct parents on callbacks and nested service calls', async (t) => {
  const left = createPageEndpoint('left'),
    right = createPageEndpoint('right')
  left.provide('side', { echo: (v) => v })
  const gates = [Promise.withResolvers(), Promise.withResolvers()]
  right.provide('work', {
    async run(i, list) {
      const call = right.captureCall()
      await gates[i].promise
      await call('side', 'echo', i)
      return list[0](i)
    },
    async returnFn(fn) {
      await tick()
      return fn
    },
  })
  const frames = connect(t, left, right)
  await tick()
  const fn = (x) => x + 1
  const calls = gates.map((_, i) => left.call('work', 'run', i, [fn]))
  await tick()
  const parents = frames.filter((f) => f.from === 'left' && f.op === 'call').map((f) => f.id)
  assert.equal(parents.length, 2)
  for (const i of [1, 0]) {
    gates[i].resolve()
    assert.equal(await calls[i], i + 1)
  }
  assert.deepEqual(
    frames.filter((f) => f.from === 'right' && f.op === 'call').map((f) => f.path),
    [[parents[1]], [parents[1]], [parents[0]], [parents[0]]],
  )
  assert.equal(await left.call('work', 'returnFn', fn), fn)
})

test('a callback receiving another callback keeps the full asynchronous call path', async (t) => {
  const left = createPageEndpoint('left'),
    right = createPageEndpoint('right')
  right.provide('work', { run: (fn) => fn((x) => x + 1) })
  const frames = connect(t, left, right)
  await tick()
  const result = await left.call('work', 'run', async (fn) => {
    await tick()
    return fn(41)
  })
  assert.equal(result, 42)
  const calls = frames.filter((f) => f.op === 'call')
  assert.deepEqual(
    calls.map((c) => c.path),
    [[], [calls[0].id], [calls[0].id, calls[1].id]],
  )
})

test('a stock protocol-5 object reference can publish and withdraw methods', async (t) => {
  const page = createPageEndpoint('page')
  const encoder = new TextEncoder()
  let near
  const peer = new Session({
    endpoint: { local: 'device' },
    send: (s) => queueMicrotask(() => near.receive(encoder.encode(s))),
    dispatch() {},
    pump() {
      throw Error('unexpected synchronous call')
    },
  })
  near = page.sessions.open({
    send: (b) => peer.receive(decode(decoder.decode(b))),
    close: () => peer.close(),
  })
  t.onTestFinished(() => {
    near.end()
    peer.close()
  })
  peer.start()
  await peer.ready
  class Sensor {
    echo(x) {
      return x
    }
  }
  await peer.invokeAsync('', 'services.announce', [
    { name: 'sensor', plugin: 'sensor', version: 1, shape: { echo: 'async' }, service: new Sensor() },
  ])
  assert.equal(await page.call('sensor', 'echo', 42), 42)
  await peer.invokeAsync('', 'services.withdraw', [{ name: 'sensor', version: 2 }])
  assert.deepEqual(page.remoteServices(), [])
})
