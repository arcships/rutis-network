// TCP byte framing for an already-admitted socket. Admission belongs to the caller.
import type { Socket } from 'node:net'

export type Close = { kind: 'done' | 'replaced' } | { kind: 'rejected', reason: string }
type MaybeAsync<T> = T | Promise<T>
export interface Sessions {
  open(far: {
    send(message: Uint8Array): Promise<void>
    close(why: Close): void
  }): MaybeAsync<{
    receive(message: Uint8Array): MaybeAsync<void>
    end(why: Close): MaybeAsync<void>
  }>
}

export const maxMessage = 16 * 1024 * 1024

function framingError(code: string, message: string) {
  return Object.assign(new Error(message), { code })
}

function checkLength(size: number) {
  if (size > maxMessage) {
    throw framingError('frame-too-large', 'TCP message exceeds 16 MiB')
  }
}

async function* readMessages(socket: Socket) {
  const header = Buffer.alloc(4)
  let body: Buffer | undefined
  let filled = 0
  for await (const chunk of socket) {
    let offset = 0
    while (offset < chunk.length) {
      const target = body ?? header
      const count = Math.min(target.length - filled, chunk.length - offset)
      chunk.copy(target, filled, offset, offset + count)
      filled += count
      offset += count
      if (filled < target.length) continue
      filled = 0
      if (body === undefined) {
        const length = header.readUInt32BE()
        checkLength(length)
        body = Buffer.allocUnsafe(length)
        if (length > 0) continue
      }
      yield body
      body = undefined
    }
  }
  if (body !== undefined || filled !== 0) {
    throw framingError('truncated-frame', 'TCP connection ended inside a frame')
  }
}

/** Resolves after open(far). Observe done for read, write or framing failures. */
export async function openTcp(socket: Socket, sessions: Sessions) {
  let near: Awaited<ReturnType<Sessions['open']>> | undefined
  let ending: Promise<void> | undefined
  let sessionClosed = false
  let failure: Error | undefined
  let writing = false

  function endSession() {
    if (near && !sessionClosed) {
      const target = near
      ending ??= Promise.resolve().then(() => target.end({ kind: 'done' }))
    }
    return ending
  }

  function remember(error: Error) {
    failure ??= error
  }

  // Handle a reset even while open or near.receive is awaiting the host.
  function closed() {
    if (!socket.readableEnded) void endSession()?.catch(remember)
  }
  socket.on('error', remember)
  socket.once('close', closed)

  async function send(message: Uint8Array) {
    try {
      checkLength(message.byteLength)
      // rutis awaits each far.send before calling the next one.
      if (writing) throw framingError('concurrent-send', 'Overlapping TCP sends')
      writing = true
      const frame = Buffer.allocUnsafe(4 + message.byteLength)
      frame.writeUInt32BE(message.byteLength)
      frame.set(message, 4)
      await new Promise<void>((resolve, reject) => {
        socket.write(frame, error => error ? reject(error) : resolve())
      })
    } catch (error) {
      remember(error as Error)
      socket.destroy()
      throw error
    } finally {
      writing = false
    }
  }

  try {
    if (socket.destroyed) throw framingError('connection-closed', 'TCP connection closed before open')
    near = await sessions.open({
      send,
      close() {
        sessionClosed = true
        socket.destroy()
      },
    })
  } catch (error) {
    socket.destroy()
    throw error
  }

  async function read(target: NonNullable<typeof near>) {
    try {
      for await (const message of readMessages(socket)) await target.receive(message)
    } catch (error) {
      // Destroying a socket during unload can interrupt its async iterator.
      if (!sessionClosed) remember(error as Error)
    } finally {
      socket.destroy()
      try {
        await endSession()
      } finally {
        socket.off('error', remember)
      }
    }
    if (failure) throw failure
  }

  return { done: read(near) }
}
