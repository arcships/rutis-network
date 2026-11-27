import { definePlugin } from '@arcships/rutis'

export default definePlugin({
  provides: { sensor: { echo: 'async', callback: 'async' } },
  apply(ctx) {
    ctx.provide('sensor', {
      echo: (value) => ['sensor', value],
      callback: (fn, value) => fn(value),
    })
  },
})
