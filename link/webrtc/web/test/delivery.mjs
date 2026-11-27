import test from 'node:test'
import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { cp, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { setTimeout as delay } from 'node:timers/promises'
import { chromium } from 'playwright'
import { build } from 'vite'
import { exec, processFor } from '../../../tcp/node/test/fixtures/hosts.mjs'

const project = fileURLToPath(new URL('../', import.meta.url))
const tokens = [randomUUID(), randomUUID()]

async function echo(pages, expected) {
  await Promise.all(
    pages.map(async (page, i) => {
      await page.getByLabel('Page endpoint ID').fill('page-' + i)
      await page.getByLabel('Pairing credential').fill(tokens[i])
      await page.getByLabel('ICE policy').selectOption('relay')
      await page.getByRole('button', { name: 'Connect', exact: true }).click()
    }),
  )
  try {
    await Promise.all(pages.map((page) => page.getByText('connected · relay', { exact: true }).waitFor()))
  } catch (error) {
    throw Error(
      'Peer states: ' +
        (await Promise.all(pages.map((page) => page.locator('#status').textContent()))).join(' / '),
      { cause: error },
    )
  }
  const page = pages[0]
  await page.getByLabel('Remote service').fill('page-1')
  // The service announcement can follow the connection status.
  for (let i = 0; i < 50; i++) {
    await page.getByRole('button', { name: 'Call echo' }).click()
    if ((await page.locator('#reply').textContent()) === JSON.stringify(expected)) return
    await delay(100)
  }
  assert.equal(await page.locator('#reply').textContent(), JSON.stringify(expected))
}

test('live browser RPC after a source edit and RPC from an installed tarball', async (t) => {
  const dir = await mkdtemp(join(tmpdir(), 'webrtc-delivery-'))
  const env = { stops: [] }
  t.after(async () => {
    try {
      for (const stop of env.stops.reverse()) await stop()
    } finally {
      await rm(dir, { recursive: true, force: true })
    }
  })
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
  const web = join(dir, 'web')
  await mkdir(web)
  for (const name of [
    'package.json',
    'package-lock.json',
    'scripts',
    'src',
    'tsdown.config.mjs',
    'tsconfig.node.json',
    'tsconfig.browser.json',
  ]) {
    await cp(join(project, name), join(web, name), { recursive: true })
  }
  await cp(join(project, '../example'), join(dir, 'example'), { recursive: true })
  await symlink(join(project, 'node_modules'), join(web, 'node_modules'), 'dir')
  const browser = await chromium.launch()
  env.stops.push(() => browser.close())
  const server = join(dir, 'example/server.mjs')
  const dev = processFor(env, process.execPath, ['--watch', server, '--dev'], { cwd: web, env: settings })
  const url = (await dev.waitFor(/http:\/\/127\.0\.0\.1:\d+/))[0]
  const pages = await Promise.all([browser.newPage(), browser.newPage()])
  assert.equal((await fetch(url + '/link-webrtc.mjs')).status, 200)
  await Promise.all(pages.map((page) => page.goto(url, { waitUntil: 'domcontentloaded' })))
  await echo(pages, ['page-1', 'hello'])

  const source = join(web, 'src/browser/endpoint.ts')
  const original = await readFile(source, 'utf8')
  const good = await (await fetch(url + '/link-webrtc.mjs')).text()
  await writeFile(source, original + '\nconst invalid = ;\n')
  await dev.waitFor(/PARSE_ERROR|Unexpected token/)
  assert.equal(await (await fetch(url + '/link-webrtc.mjs')).text(), good)
  const page = pages[0]
  await page.locator('#reply').evaluate((output) => {
    output.value = ''
  })
  await page.getByRole('button', { name: 'Call echo' }).click()
  await page.getByText(JSON.stringify(['page-1', 'hello']), { exact: true }).waitFor()
  const call = 'return invoke(methodCall, args)'
  assert.ok(original.includes(call))
  await Promise.all([
    ...pages.map((page) => page.waitForEvent('domcontentloaded')),
    writeFile(source, original.replace(call, "return ['updated', await invoke(methodCall, args)]")),
  ])
  await echo(pages, ['updated', ['page-1', 'hello']])
  const html = join(dir, 'example/index.html')
  const originalHtml = await readFile(html, 'utf8')
  await writeFile(html, originalHtml.replace('Connect a rutis peer', 'Updated pairing page'))
  await Promise.all(
    pages.map((page) => page.getByRole('heading', { name: 'Updated pairing page' }).waitFor()),
  )
  await Promise.all(pages.map((page) => page.close()))

  const pairing = join(web, 'src/node/pairing.ts')
  const originalPairing = await readFile(pairing, 'utf8')
  const grantId = 'attemptId: current.id'
  assert.ok(originalPairing.includes(grantId))
  const offset = dev.output().length
  await writeFile(pairing, originalPairing.replace(grantId, "attemptId: 'reloaded-' + current.id"))
  const restarted = (await dev.waitFor(/http:\/\/127\.0\.0\.1:\d+/, offset))[0]
  const grant = await fetch(restarted + '/signal/acquire', {
    method: 'POST',
    headers: { authorization: 'Bearer ' + tokens[0], 'content-type': 'application/json' },
    body: JSON.stringify({ pairId: 'desk-and-page' }),
  })
  assert.match((await grant.json()).attemptId, /^reloaded-/)
  await writeFile(pairing, originalPairing)
  await writeFile(source, original)
  const consumer = join(dir, 'consumer')
  await mkdir(consumer)
  const { stdout } = await exec('npm', ['pack', '--json', '--pack-destination', consumer], { cwd: web })
  const [{ filename: artifact }] = JSON.parse(stdout)
  await writeFile(join(consumer, 'package.json'), '{"type":"module","private":true}')
  await exec('npm', ['install', '--omit=dev', '--no-audit', './' + artifact], { cwd: consumer })
  const entry = join(consumer, 'app.ts')
  await writeFile(
    entry,
    `import { createWebRtcLink, createPageEndpoint, httpSignaling } from '@arcships/rutis-link-webrtc'
export { createWebRtcLink, createPageEndpoint, httpSignaling }
export function consumer(pairId: string) {
  const page = createPageEndpoint('page')
  const signaling = httpSignaling('/signal')
  const link = createWebRtcLink({ config: { pairId, autoStart: false }, sessions: page.sessions, signaling })
  const poll = () => signaling.acquire({ pairId, after: null, waitMs: 100 })
  const near = page.sessions.open({ send() {}, close() {} })
  near.end({ kind: 'done' })
  return { link, poll }
}
`,
  )
  await exec(
    join(project, 'node_modules/.bin/tsc'),
    [
      '--strict',
      '--skipLibCheck',
      'false',
      '--target',
      'es2022',
      '--module',
      'nodenext',
      '--noEmit',
      'app.ts',
    ],
    { cwd: consumer },
  )
  await build({
    configFile: false,
    root: consumer,
    build: {
      target: 'es2022',
      outDir: join(dir, 'dist'),
      emptyOutDir: false,
      lib: { entry, formats: ['es'], fileName: () => 'link-webrtc.mjs' },
    },
  })
  // Serve the consumer's bundle at the example's public URL.
  const packed = processFor(env, process.execPath, [server], { cwd: dir, env: settings })
  const packedURL = (await packed.waitFor(/http:\/\/127\.0\.0\.1:\d+/))[0]
  const installed = await Promise.all([browser.newPage(), browser.newPage()])
  await Promise.all(installed.map((page) => page.goto(packedURL)))
  await echo(installed, ['page-1', 'hello'])
})
