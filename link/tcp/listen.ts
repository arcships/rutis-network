import { definePlugin } from '@arcships/rutis'
import { once } from 'node:events'
import type { AddressInfo, Socket } from 'node:net'
import { setTimeout as delay } from 'node:timers/promises'
import { createServer, type Server } from 'node:tls'
import { credentials, endpointSchema, errorCode, type Endpoint } from './adapter.ts'
import { openTcp, type Sessions } from './bridge.ts'

export default definePlugin<Endpoint>({
  inject: ['rutis/sessions'],
  config: endpointSchema(0),
  async apply(ctx, config) {
    const identity = await credentials(config)
    ctx.signal.throwIfAborted()
    const sessions = ctx.use<Sessions>('rutis/sessions')
    const controller = new AbortController()
    const signal = AbortSignal.any([ctx.signal, controller.signal])
    const sockets = new Set<Socket>()
    let listener: Server
    let port = config.port
    ctx.effect(() => {
      controller.abort()
      listener?.close()
      for (const socket of sockets) socket.destroy()
    })

    function report(error: unknown) {
      if (!signal.aborted) console.error(`link/tcp listen 127.0.0.1:${port} ${errorCode(error)}`)
    }

    async function bind() {
      signal.throwIfAborted()
      const server = createServer({
        ...identity, requestCert: true, rejectUnauthorized: true, handshakeTimeout: 10_000,
      }, socket => {
        if (signal.aborted) {
          socket.destroy()
          return
        }
        void openTcp(socket, sessions).then(link => link.done).catch(report)
      })
      listener = server
      // Track raw sockets from accept, including clients still negotiating TLS.
      server.on('connection', socket => {
        sockets.add(socket)
        socket.once('close', () => sockets.delete(socket))
      })
      server.on('tlsClientError', report)
      server.listen({ host: '127.0.0.1', port, signal })
      try {
        await once(server, 'listening', { signal })
        signal.throwIfAborted()
      } catch (error) {
        server.close()
        throw error
      }
      port = (server.address() as AddressInfo).port
      console.log(`link/tcp listening 127.0.0.1:${port}`)
      server.once('error', error => {
        report(error)
        server.close()
        void rebind().catch(report)
      })
    }

    // Stop accepting on a failed listener. Existing sessions keep their sockets.
    async function rebind() {
      let wait = 500
      while (!signal.aborted) {
        await delay(wait, undefined, { signal })
        try {
          await bind()
          return
        } catch (error) {
          report(error)
          wait = Math.min(30_000, wait * 2)
        }
      }
    }

    await bind()
  },
})
