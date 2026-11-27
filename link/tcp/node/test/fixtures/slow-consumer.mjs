import { access } from 'node:fs/promises'
import { setTimeout as delay } from 'node:timers/promises'
import { definePlugin } from '@arcships/rutis'

export default definePlugin({
  inject: ['witness'],
  apply(ctx, { release }) {
    ctx.effect(async () => {
      console.log('waiting for consumer cleanup')
      for (;;) {
        try {
          await access(release)
          return
        } catch (error) {
          if (error.code !== 'ENOENT') throw error
        }
        await delay(20)
      }
    })
  },
})
