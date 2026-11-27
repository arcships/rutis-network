import assert from 'node:assert/strict'
import test from 'node:test'
import { once } from 'node:events'
import { createConnection, createServer } from 'node:net'
import { setImmediate } from 'node:timers/promises'
import { maxMessage, openTcp } from '../../bridge.ts'

function frame(bytes) {
  const header = Buffer.alloc(4)
  header.writeUInt32BE(bytes.length)
  return Buffer.concat([header, bytes])
}

async function pair(t) {
  const server = createServer()
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const accepted = once(server, 'connection')
  const peer = createConnection(server.address().port, '127.0.0.1')
  const [socket] = await accepted
  t.after(() => { socket.destroy(); peer.destroy(); server.close() })
  return { socket, peer }
}

async function attach(socket, receive = () => {}) {
  const ends = []
  let far
  const link = await openTcp(socket, { open(callbacks) {
    far = callbacks
    return { receive, end: why => { ends.push(why) } }
  } })
  return { ...link, ends, far }
}

test('split headers, coalesced and empty messages, ordered asynchronous receives', async t => {
  const { socket, peer } = await pair(t)
  const received = []
  const first = Promise.withResolvers()
  const release = Promise.withResolvers()
  const { done, ends } = await attach(socket, async bytes => {
    received.push(bytes.toString())
    if (received.length === 1) {
      first.resolve()
      await release.promise
    }
  })
  const messages = ['first', '', 'last'].map(text => frame(Buffer.from(text)))
  peer.write(messages[0].subarray(0, 2))
  await setImmediate()
  peer.end(Buffer.concat([messages[0].subarray(2), ...messages.slice(1)]))
  await first.promise
  await setImmediate()
  assert.deepEqual(received, ['first'])
  release.resolve()
  await done
  assert.deepEqual(received, ['first', '', 'last'])
  assert.deepEqual(ends, [{ kind: 'done' }])
})

for (const [name, bytes, code] of [
  ['partial header', Buffer.from([0, 0]), 'truncated-frame'],
  ['partial payload', Buffer.from([0, 0, 0, 9, 1]), 'truncated-frame'],
  ['oversized header', Buffer.from([1, 0, 0, 1]), 'frame-too-large'],
]) {
  test(name + ' ends the connection without delivering a message', async t => {
    const { socket, peer } = await pair(t)
    const { done, ends } = await attach(socket, () => assert.fail('unexpected payload'))
    peer.end(bytes)
    await assert.rejects(done, { code })
    assert.deepEqual(ends, [{ kind: 'done' }])
  })
}

test('16 MiB passes in both directions with the writer awaiting socket completion', async t => {
  const { socket, peer } = await pair(t)
  const payload = Buffer.alloc(maxMessage, 0x71)
  const incoming = Promise.withResolvers()
  const { far, done } = await attach(socket, bytes => {
    assert.deepEqual(bytes, payload)
    incoming.resolve()
  })
  peer.pause()
  let sent = false
  const send = far.send(payload).then(() => { sent = true })
  await setImmediate()
  assert.equal(sent, false)
  const received = []
  peer.on('data', bytes => received.push(bytes))
  peer.resume()
  await send
  peer.write(frame(payload))
  await incoming.promise
  peer.end()
  const closed = once(peer, 'close')
  await done
  await closed
  assert.deepEqual(Buffer.concat(received), frame(payload))
})

test('failed outgoing frame closes the socket and preserves its error', async t => {
  const { socket } = await pair(t)
  const { far, done, ends } = await attach(socket)
  await assert.rejects(far.send(Buffer.alloc(maxMessage + 1)), { code: 'frame-too-large' })
  await assert.rejects(done, { code: 'frame-too-large' })
  assert.equal(ends.length, 1)
})

test('session close during open releases TCP without echoing end', async t => {
  const { socket, peer } = await pair(t)
  const closed = once(peer, 'close')
  const { done } = await openTcp(socket, { open(far) {
    far.close({ kind: 'done' })
    far.close({ kind: 'done' })
    return { receive: assert.fail, end: assert.fail }
  } })
  await done
  await closed
})

test('socket is released if mounting the session fails', async t => {
  const { socket, peer } = await pair(t)
  const closed = once(peer, 'close')
  await assert.rejects(openTcp(socket, { open() { throw new Error('mount failed') } }), /mount failed/)
  await closed
})

test('a local teardown unblocks a pending receive and ends the session once', async t => {
  const { socket, peer } = await pair(t)
  const receiving = Promise.withResolvers()
  const release = Promise.withResolvers()
  let ends = 0
  const { done } = await openTcp(socket, { open: () => ({
    receive() { receiving.resolve(); return release.promise },
    end() { ends++; release.resolve() },
  }) })
  peer.write(frame(Buffer.from('hello')))
  await receiving.promise
  socket.destroy()
  await done.catch(error => assert.equal(error.code, 'ERR_STREAM_PREMATURE_CLOSE'))
  assert.equal(ends, 1)
})

test('peer closing while open is pending still ends the returned session', async t => {
  const { socket, peer } = await pair(t)
  const opening = Promise.withResolvers()
  const link = openTcp(socket, { open: () => opening.promise })
  peer.end()
  const ended = []
  opening.resolve({ receive: assert.fail, end: why => ended.push(why) })
  await (await link).done
  assert.deepEqual(ended, [{ kind: 'done' }])
})

test('truncated input keeps its diagnostic if session cleanup also fails', async t => {
  const { socket, peer } = await pair(t)
  const link = await openTcp(socket, { open: () => ({
    receive: assert.fail,
    end() { throw new Error('session already gone') },
  }) })
  peer.end(Buffer.from([0, 0]))
  await assert.rejects(link.done, { code: 'truncated-frame' })
})

test('1,000 binary messages echo in order over the same TCP connection', async t => {
  const { socket, peer } = await pair(t)
  let count = 0
  const complete = Promise.withResolvers()
  const link = await attach(socket, async message => {
    assert.equal(message.readUInt32BE(0), count++)
    await link.far.send(message)
    if (count === 1000) complete.resolve()
  })
  const frames = []
  for (let n = 0; n < 1000; n++) {
    const value = Buffer.alloc(4)
    value.writeUInt32BE(n)
    frames.push(frame(value))
  }
  const wire = Buffer.concat(frames)
  const echoes = []
  peer.on('data', chunk => echoes.push(chunk))
  const closed = once(peer, 'close')
  peer.write(wire)
  await Promise.race([complete.promise, link.done])
  peer.end()
  await link.done
  await closed
  assert.equal(count, 1000)
  assert.deepEqual(Buffer.concat(echoes), wire)
})

test('a peer reset releases the session with the socket error code', async t => {
  const { socket, peer } = await pair(t)
  const { done, ends } = await attach(socket)
  peer.resetAndDestroy()
  await assert.rejects(done, { code: 'ECONNRESET' })
  assert.deepEqual(ends, [{ kind: 'done' }])
})
