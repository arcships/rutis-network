import { randomBytes } from 'node:crypto'
import { definePlugin } from '@arcships/rutis'

const chunkSize = 4 * 1024 * 1024

// The application controls the chunks. The CLI receives only a small receipt.
export default definePlugin({
  inject: ['witness'],
  provides: { probe: { roundTripChunks: 'async' } },
  apply(ctx) {
    const witness = ctx.use('witness')
    ctx.provide('probe', {
      async roundTripChunks(size) {
        const source = randomBytes(size)
        const received = Buffer.alloc(size)
        let proof
        for (let offset = 0; offset < size; offset += chunkSize) {
          const chunk = source.subarray(offset, offset + chunkSize)
          const [peer, echoed] = await witness.echo(chunk)
          if (proof !== undefined && peer !== proof) throw new Error('echo peer changed')
          if (echoed.byteLength !== chunk.byteLength) throw new Error('echo length changed')
          received.set(echoed, offset)
          proof = peer
        }
        if (!source.equals(received)) throw new Error('reassembled payload changed')
        return [proof, received.byteLength]
      },
    })
  },
})
