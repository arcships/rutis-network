import test from 'node:test'
import assert from 'node:assert/strict'
import { cp, mkdir, readFile, symlink, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { processFor } from '../../../tcp/node/test/fixtures/hosts.mjs'
import { project, setup } from './fixtures/delivery.mjs'

async function echo(pages, tokens, expected) {
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

test('live browser RPC and signaling reload after source edits', async (t) => {
  const { root, dir, env, settings, tokens, browser } = await setup(t)
  await cp(join(project, '../../../tsconfig.base.json'), join(root, 'tsconfig.base.json'))
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
  await symlink(join(project, 'node_modules'), join(web, 'node_modules'), 'dir')
  const server = join(dir, 'example/server.mjs')
  const dev = processFor(env, process.execPath, ['--watch', server, '--dev'], { cwd: web, env: settings })
  const url = (await dev.waitFor(/http:\/\/127\.0\.0\.1:\d+/))[0]
  const pages = await Promise.all([browser.newPage(), browser.newPage()])
  assert.equal((await fetch(url + '/link-webrtc.mjs')).status, 200)
  await Promise.all(pages.map((page) => page.goto(url, { waitUntil: 'domcontentloaded' })))
  await echo(pages, tokens, ['page-1', 'hello'])

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
  await echo(pages, tokens, ['updated', ['page-1', 'hello']])
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
})
