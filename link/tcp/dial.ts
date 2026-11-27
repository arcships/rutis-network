// A single admitted connection for the reference example. A reconnecting adapter
// calls openTcp with a fresh socket after each successful admission.
import { definePlugin } from '@arcships/rutis'
import { once } from 'node:events'
import { readFile } from 'node:fs/promises'
import { connect } from 'node:tls'
import { openTcp, type Sessions } from './bridge.ts'

interface Config { port: number, ca: string, cert: string, key: string }

export default definePlugin<Config>({
  inject: ['rutis/sessions'],
  config: {
    type: 'object', required: ['port', 'ca', 'cert', 'key'],
    properties: {
      port: { type: 'integer', minimum: 1, maximum: 65535 },
      ca: { type: 'string' }, cert: { type: 'string' }, key: { type: 'string' },
    },
  },
  async apply(ctx, config) {
    const [ca, cert, key] = await Promise.all([config.ca, config.cert, config.key].map(path => readFile(path)))
    const socket = connect({
      host: '127.0.0.1', servername: 'localhost', port: config.port,
      ca, cert, key, rejectUnauthorized: true,
    })
    ctx.effect(() => { socket.destroy() })
    try {
      await once(socket, 'secureConnect', { signal: AbortSignal.any([ctx.signal, AbortSignal.timeout(10_000)]) })
      const link = await openTcp(socket, ctx.use<Sessions>('rutis/sessions'))
      link.done.catch((error: NodeJS.ErrnoException) => {
        if (!ctx.signal.aborted) console.error(`link/tcp 127.0.0.1:${config.port} ${error.code ?? 'connection-failed'}`)
      })
    } catch (error) {
      socket.destroy()
      throw error
    }
  },
})
