import test from 'node:test'
import assert from 'node:assert/strict'
import { attachChannel } from '../browser/channel.mjs'
import { createPairing } from '../pairing.mjs'
import { createWebRtcLink } from '../browser/link.mjs'

test('one bounded message each way, slow consumer, close and invalid records', async () => {
  const lifeA = new AbortController(),
    lifeB = new AbortController()
  const a = { readyState: 'open', bufferedAmount: 0 },
    b = { ...a }
  a.send = (record) => b.onmessage({ data: record.slice().buffer })
  b.send = (record) => a.onmessage({ data: record.slice().buffer })
  const left = attachChannel(a, lifeA),
    right = attachChannel(b, lifeB)
  let sendLeft,
    sendRight,
    received = [],
    hold
  const open = (save) => ({
    open(far) {
      save(far.send)
      return {
        receive: async (bytes) => {
          received.push(bytes)
          if (hold) await hold.promise
        },
        end() {},
      }
    },
  })
  const runs = Promise.allSettled([
    left.serve(
      open((send) => {
        sendLeft = send
      }),
      16384,
      () => {},
    ),
    right.serve(
      open((send) => {
        sendRight = send
      }),
      16384,
      () => {},
    ),
  ])
  await Promise.all([sendLeft(new Uint8Array()), sendRight(Uint8Array.of(93))])
  assert.deepEqual(received.map((b) => b.length).sort(), [0, 1])
  const large = new Uint8Array(16 << 20)
  large.fill(214)
  await sendLeft(large)
  assert.deepEqual(received.at(-1), large)
  hold = Promise.withResolvers()
  let completed = false
  const pending = sendLeft(Uint8Array.of(1)).then(() => {
    completed = true
  })
  await new Promise((r) => setImmediate(r))
  assert.equal(completed, false)
  hold.resolve()
  await pending
  hold = Promise.withResolvers()
  const blocked = sendRight(Uint8Array.of(2))
  a.onmessage({ data: 'invalid' })
  assert.equal(lifeA.signal.reason.message, 'incompatible-channel')
  lifeB.abort(Error('closed'))
  await assert.rejects(blocked)
  await runs
  hold.resolve()
})

test('pairing authorization, replay, expiry and repeat delivery', async () => {
  const pairing = createPairing({ pairId: 'room', members: ['a', 'b'] })
  const fields = { pairId: 'room', after: null, waitMs: 0 }
  await assert.rejects(pairing('other', 'acquire', fields), { name: 'PairingDenied' })
  const a = await pairing('a', 'acquire', fields),
    b = await pairing('b', 'acquire', fields)
  assert.equal(a.attemptId, b.attemptId)
  assert.notEqual(a.role, b.role)
  const offer = { ...fields, attemptId: a.attemptId, type: 'offer', sdp: 'example' }
  await assert.rejects(pairing('b', 'publish', offer))
  await pairing('a', 'publish', offer)
  await pairing('a', 'publish', offer)
  await assert.rejects(pairing('a', 'publish', { ...offer, sdp: 'different' }))
  assert.deepEqual(await pairing('b', 'receive', offer), { type: 'offer', sdp: 'example' })
  assert.notEqual((await pairing('a', 'acquire', { ...fields, after: a.attemptId })).attemptId, a.attemptId)
  await assert.rejects(pairing('b', 'receive', offer))
  const expiring = createPairing({ pairId: 'room', members: ['a', 'b'], lifetimeMs: 5 })
  const old = await expiring('a', 'acquire', fields)
  await new Promise((r) => setTimeout(r, 15))
  await assert.rejects(expiring('a', 'publish', { ...offer, attemptId: old.attemptId }))
})

test('stopping a pending pairing ignores late replies; denial fails before session open', async () => {
  const pending = Promise.withResolvers()
  let deny = false,
    opened = 0
  const signaling = {
    acquire: () =>
      deny ? Promise.reject(Object.assign(Error(), { name: 'PairingDenied' })) : pending.promise,
    publish() {},
    receive() {},
  }
  const link = createWebRtcLink({
    sessions: {
      open() {
        opened++
      },
    },
    signaling,
    config: { pairId: 'one' },
  })
  const cursor = link.status().cursor
  const change = link.waitForChange(cursor)
  await link.stop()
  assert.equal((await change).phase, 'stopped')
  pending.resolve({ attemptId: 'stale' })
  await link.configure({ pairId: 'two' })
  deny = true
  await link.start()
  while (link.status().phase !== 'failed') await link.waitForChange(link.status().cursor)
  assert.equal(link.status().lastError, 'pairing-denied')
  assert.equal(opened, 0)
  const before = link.status()
  await assert.rejects(link.configure({ pairId: 'three', iceTransportPolicy: 'invalid' }))
  assert.deepEqual(link.status(), before)
  await link.stop()
})

test('an oversized send closes the wire before emitting a record', async () => {
  const lifetime = new AbortController()
  const wire = attachChannel(
    {
      readyState: 'open',
      bufferedAmount: 0,
      send() {
        assert.fail('oversized record was sent')
      },
    },
    lifetime,
  )
  let far,
    ended = 0
  const ready = Promise.withResolvers()
  const run = wire.serve(
    {
      open(sender) {
        far = sender
        return {
          end() {
            ended++
          },
        }
      },
    },
    16384,
    ready.resolve,
  )
  await ready.promise
  await assert.rejects(far.send(new Uint8Array((16 << 20) + 1)), /incompatible-channel/)
  await assert.rejects(run, /incompatible-channel/)
  assert.equal(ended, 1)
})

test('switching pair during backoff cancels the old retry and does not open a stale session', async () => {
  const pairs = []
  const pending = Promise.withResolvers()
  const link = createWebRtcLink({
    sessions: { open: () => assert.fail('unexpected session') },
    signaling: {
      acquire({ pairId }) {
        pairs.push(pairId)
        if (pairId === 'offline') throw Error('unreachable')
        return pending.promise
      },
      publish() {},
      receive() {},
    },
    config: { pairId: 'offline' },
  })
  while (link.status().phase !== 'retrying') await link.waitForChange(link.status().cursor)
  await link.configure({ pairId: 'replacement' })
  await link.stop()
  pending.resolve(null)
  assert.deepEqual(pairs, ['offline', 'replacement'])
  assert.equal(link.status().phase, 'stopped')
})
