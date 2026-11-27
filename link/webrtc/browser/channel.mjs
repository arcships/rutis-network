const recordLimit = 16 * 1024
const messageLimit = 16 * 1024 * 1024
const highWater = 256 * 1024

export function abortable(promise, signal) {
  if (signal.aborted) {
    Promise.resolve(promise).catch(() => {})
    return Promise.reject(signal.reason)
  }
  return new Promise((resolve, reject) => {
    const abort = () => reject(signal.reason)
    signal.addEventListener('abort', abort, { once: true })
    Promise.resolve(promise)
      .then(resolve, reject)
      .finally(() => signal.removeEventListener('abort', abort))
  })
}

export function delay(ms, signal) {
  return new Promise((resolve, reject) => {
    signal.throwIfAborted()
    const abort = () => {
      clearTimeout(timer)
      reject(signal.reason)
    }
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', abort)
      resolve()
    }, ms)
    signal.addEventListener('abort', abort, { once: true })
  })
}

export function attachChannel(dc, lifetime) {
  const { signal } = lifetime
  const fail = (code) => lifetime.abort(new Error(code))
  dc.binaryType = 'arraybuffer'
  dc.bufferedAmountLowThreshold = highWater / 2
  const opened = Promise.withResolvers()
  const mounted = Promise.withResolvers()
  let ack,
    writing = false,
    delivering = false,
    partial,
    used = 0,
    near,
    closedBySession = false
  dc.onopen = () => opened.resolve()
  if (dc.readyState === 'open') opened.resolve()
  dc.onclose = dc.onerror = () => fail('channel-closed')
  function accept(record) {
    if (record.length === 0 || record.length > recordLimit) throw Error()
    if (record[0] === 3) {
      if (record.length !== 1 || !ack) throw Error()
      ack.resolve()
      ack = undefined
      return
    }
    let offset
    if (record[0] === 1) {
      if (partial || delivering || record.length < 5) throw Error()
      const size = new DataView(record.buffer, record.byteOffset).getUint32(1)
      if (size > messageLimit || (size > 0 && record.length === 5)) throw Error()
      partial = new Uint8Array(size)
      used = 0
      offset = 5
    } else if (record[0] === 2 && partial && record.length > 1) {
      offset = 1
    } else throw Error()
    partial.set(record.subarray(offset), used)
    used += record.length - offset
    if (used !== partial.length) return
    const data = partial
    partial = undefined
    delivering = true
    void (async () => {
      const receiver = await abortable(mounted.promise, signal)
      await abortable(receiver.receive(data), signal)
      signal.throwIfAborted()
      delivering = false
      dc.send(Uint8Array.of(3))
    })().catch(() => fail('channel-closed'))
  }
  dc.onmessage = ({ data }) => {
    if (signal.aborted) return
    try {
      if (!(data instanceof ArrayBuffer)) throw Error()
      accept(new Uint8Array(data))
    } catch {
      fail('incompatible-channel')
    }
  }
  async function send(data, limit) {
    if (writing || !(data instanceof Uint8Array) || data.length > messageLimit || limit < 6) {
      fail('incompatible-channel')
      throw signal.reason
    }
    writing = true
    try {
      const done = Promise.withResolvers()
      let offset = 0,
        first = true
      do {
        signal.throwIfAborted()
        if (dc.bufferedAmount > highWater) {
          const drained = Promise.withResolvers()
          dc.onbufferedamountlow = drained.resolve
          try {
            await abortable(drained.promise, signal)
          } finally {
            dc.onbufferedamountlow = null
          }
        }
        const header = first ? 5 : 1
        const n = Math.min(limit - header, data.length - offset)
        const record = new Uint8Array(header + n)
        record[0] = first ? 1 : 2
        if (first) new DataView(record.buffer).setUint32(1, data.length)
        record.set(data.subarray(offset, offset + n), header)
        offset += n
        first = false
        if (offset === data.length) ack = done
        dc.send(record)
      } while (offset < data.length)
      await abortable(done.promise, signal)
    } catch (e) {
      fail('channel-closed')
      throw e
    } finally {
      writing = false
    }
  }
  return {
    opened: opened.promise,
    async serve(sessions, maxSize, connected) {
      signal.throwIfAborted()
      const limit = Math.min(recordLimit, maxSize || recordLimit)
      if (limit < 6) throw Error('incompatible-channel')
      try {
        near = await sessions.open({
          send: (data) => send(data, limit),
          close() {
            closedBySession = true
            fail('channel-closed')
          },
        })
        signal.throwIfAborted()
        mounted.resolve(near)
        connected()
        signal.throwIfAborted()
        await new Promise((_, reject) =>
          signal.addEventListener('abort', () => reject(signal.reason), { once: true }),
        )
      } finally {
        if (near && !closedBySession) await near.end({ kind: 'done' })
      }
    },
  }
}
