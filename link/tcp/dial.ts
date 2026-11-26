import { definePlugin } from '@arcships/rutis'
import { once } from 'node:events'
import { setTimeout as delay } from 'node:timers/promises'
import { connect } from 'node:tls'
import { credentials, endpointSchema, errorCode, type Endpoint } from './adapter.ts'
import { openTcp, type Sessions } from './bridge.ts'

interface Config extends Endpoint { reconnect?: boolean, servername?: string }

async function dial(config: Config, sessions: Sessions, signal: AbortSignal) {
  const identity = await credentials(config)
  signal.throwIfAborted()
  const socket = connect({
    ...identity, host: '127.0.0.1', servername: config.servername ?? 'localhost',
    port: config.port, rejectUnauthorized: true,
  })
  const close = () => { socket.destroy() }
  signal.addEventListener('abort', close, { once: true })
  socket.once('close', () => signal.removeEventListener('abort', close))
  try {
    await once(socket, 'secureConnect', { signal: AbortSignal.any([signal, AbortSignal.timeout(10_000)]) })
    signal.throwIfAborted()
    return await openTcp(socket, sessions)
  } catch (error) {
    socket.destroy()
    throw error
  }
}

// Only classify errors reported by the adapter. open(far) has no handshake result.
export function retryDelay(code: string, backoff: number) {
  if (['ERR_SSL_WRONG_VERSION_NUMBER', 'ERR_SSL_UNSUPPORTED_PROTOCOL',
    'ERR_SSL_TLSV1_ALERT_PROTOCOL_VERSION'].includes(code)) return null
  if (/CERT|SELF_SIGNED|UNKNOWN_CA|UNABLE_TO_VERIFY|UNABLE_TO_GET_ISSUER/.test(code)) return 30_000
  return backoff
}

async function reconnect(config: Config, sessions: Sessions, signal: AbortSignal) {
  let backoff = 500
  while (!signal.aborted) {
    let connectedAt: number | undefined
    let code = 'connection-closed'
    try {
      const link = await dial(config, sessions, signal)
      connectedAt = performance.now()
      await link.done
    } catch (error) {
      code = errorCode(error)
    }
    if (signal.aborted) return
    if (connectedAt !== undefined && performance.now() - connectedAt >= 60_000) backoff = 500
    const wait = retryDelay(code, backoff)
    const reason = wait === null ? 'stopped' : `retry in ${wait}ms`
    console.error(`link/tcp 127.0.0.1:${config.port} ${code} (${reason})`)
    if (wait === null) return
    await delay(wait, undefined, { signal })
    backoff = Math.min(30_000, backoff * 2)
  }
}

export default definePlugin<Config>({
  inject: ['rutis/sessions'],
  config: endpointSchema(1, { reconnect: { type: 'boolean' }, servername: { type: 'string', minLength: 1 } }),
  async apply(ctx, config) {
    const controller = new AbortController()
    ctx.effect(() => controller.abort())
    const signal = AbortSignal.any([ctx.signal, controller.signal])
    const sessions = ctx.use<Sessions>('rutis/sessions')
    const report = (error: unknown) => {
      if (!signal.aborted) console.error(`link/tcp 127.0.0.1:${config.port} ${errorCode(error)}`)
    }
    if (config.reconnect) {
      void reconnect(config, sessions, signal).catch(report)
    } else {
      const link = await dial(config, sessions, signal)
      void link.done.catch(report)
    }
  },
})
