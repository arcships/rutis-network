import { definePlugin } from '@arcships/rutis'

// Keep the large value on the host-to-host route. The CLI receives a small receipt.
export default definePlugin({
  inject: ['witness'],
  provides: { probe: { roundTrip: 'async' } },
  apply(ctx) {
    const witness = ctx.use('witness')
    ctx.provide('probe', {
      async roundTrip(size) {
        const payload = Buffer.alloc(size, 0xa7)
        const [proof, echoed] = await witness.echo(payload)
        if (!payload.equals(echoed)) throw new Error('round-trip payload changed')
        return [proof, echoed.byteLength]
      },
    })
  },
})
