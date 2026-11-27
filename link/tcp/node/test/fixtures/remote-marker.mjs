import { definePlugin } from '@arcships/rutis'

export default definePlugin({
  provides: { 'routed-marker': { pid: 'async' } },
  apply(ctx) {
    ctx.provide('routed-marker', { pid: () => process.pid })
  },
})
