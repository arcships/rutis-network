import { definePlugin } from '@arcships/rutis'
import { readFile } from 'node:fs/promises'
import { createServer } from 'node:tls'
import { once } from 'node:events'
import { openTcp } from '../../bridge.ts'

// Only authenticated clients reach secureConnection. A per-test CA limits admission.
export default definePlugin({
  inject: ['rutis/sessions'],
  provides: { witness: { echo: 'async' } },
  async apply(ctx, { cert, key, proof }) {
    const certificate = await readFile(cert)
    const server = createServer({
      ca: certificate, cert: certificate, key: await readFile(key),
      requestCert: true, rejectUnauthorized: true,
    })
    const sockets = new Set()
    ctx.effect(() => {
      server.close()
      for (const socket of sockets) socket.destroy()
    })
    server.on('secureConnection', socket => {
      sockets.add(socket)
      socket.once('close', () => {
        sockets.delete(socket)
        console.log('LINK_CLOSED')
      })
      openTcp(socket, ctx.use('rutis/sessions')).then(link => link.done).catch(error => {
        if (!ctx.signal.aborted) console.error(error.code ?? 'connection-failed')
      })
    })
    server.listen(0, '127.0.0.1')
    await once(server, 'listening')
    ctx.provide('witness', { echo: value => [proof, value] })
    console.log('LINK_LISTENING ' + server.address().port)
  },
})
