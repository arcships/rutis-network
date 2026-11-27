import assert from 'node:assert/strict'
import { request } from 'node:http'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { row } from './hosts.mjs'

export function manager(env, id, service, isolate) {
  const socket = join(env.dir, id + '.sock')
  const ui = {
    ...row(id + '-ui', 'test/fixtures/controller.mjs', { service, socket }),
    inject: [service],
    isolate,
  }
  function invoke(method, ...args) {
    return new Promise((resolve, reject) => {
      const req = request({ socketPath: socket, method: 'POST' }, async (res) => {
        try {
          const chunks = []
          for await (const chunk of res) chunks.push(chunk)
          const text = Buffer.concat(chunks).toString()
          if (res.statusCode !== 200) throw new Error(text)
          resolve(JSON.parse(text))
        } catch (error) {
          reject(error)
        }
      })
      req.on('error', reject)
      req.end(JSON.stringify([method, ...args]))
    })
  }
  async function until(check) {
    for (let i = 0; i < 150; i++) {
      const s = await invoke('status')
      if (check(s)) return s
      await delay(25)
    }
    assert.fail('unexpected ' + id + ' state: ' + JSON.stringify(await invoke('status')))
  }
  return { invoke, until, ui }
}
