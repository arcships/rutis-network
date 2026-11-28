import { randomUUID } from 'node:crypto'
import { cp, mkdir, mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'
import { exec, processFor } from '../../../../tcp/node/test/fixtures/hosts.mjs'

export const project = fileURLToPath(new URL('../../', import.meta.url))

export async function setup(t) {
  const tokens = [randomUUID(), randomUUID()]
  const root = await mkdtemp(join(tmpdir(), 'webrtc-delivery-'))
  const dir = join(root, 'link/webrtc')
  const env = { stops: [] }
  t.after(async () => {
    try {
      for (const stop of env.stops.reverse()) await stop()
    } finally {
      await rm(root, { recursive: true, force: true })
    }
  })
  await mkdir(dir, { recursive: true })
  const turn = join(dir, 'turn')
  await exec('go', ['build', '-o', turn, './example/turn'], { cwd: join(project, '..') })
  const turnSecret = randomUUID()
  const relay = processFor(env, turn, [], { env: { ...process.env, TURN_SECRET: turnSecret } })
  const port = (await relay.waitFor(/TURN [\d.]+:(\d+)/))[1]
  const settings = {
    ...process.env,
    PORT: '0',
    PAIR_ID: 'desk-and-page',
    PAIR_A_TOKEN: tokens[0],
    PAIR_B_TOKEN: tokens[1],
    TURN_URL: 'turn:127.0.0.1:' + port + '?transport=udp',
    TURN_SECRET: turnSecret,
  }
  await cp(join(project, '../example'), join(dir, 'example'), { recursive: true })
  const browser = await chromium.launch()
  env.stops.push(() => browser.close())
  return { root, dir, browser, settings, tokens, env }
}
