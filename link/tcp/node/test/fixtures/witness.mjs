import { definePlugin } from '@arcships/rutis'

export default definePlugin({
  provides: { witness: { echo: 'async', pid: 'async' } },
  apply(ctx, { proof }) {
    ctx.provide('witness', { echo: (value) => [proof, value], pid: () => process.pid })
  },
})
