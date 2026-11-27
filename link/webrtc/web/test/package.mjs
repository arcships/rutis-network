import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { copyFile, cp, mkdir, readFile, stat, writeFile } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import test from 'node:test'
import { exec, processFor } from '../../../tcp/node/test/fixtures/hosts.mjs'
import { project, setup } from './fixtures/delivery.mjs'

test(
  'install the browser tarball in an external strict TypeScript app and call a remote peer',
  { timeout: 300000 },
  async (t) => {
    const { root, dir, browser, settings, tokens, env } = await setup(t)
    let archive = process.env.WEBRTC_TARBALL
    if (!archive) {
      const { stdout } = await exec('npm', ['pack', '--json', '--pack-destination', root], { cwd: project })
      archive = join(root, JSON.parse(stdout)[0].filename)
    }

    assert.ok((await stat(archive)).isFile(), 'Expected an npm tarball file')
    const consumer = join(root, 'consumer')
    await cp(new URL('./fixtures/consumer/', import.meta.url), consumer, { recursive: true })
    const { devDependencies } = JSON.parse(await readFile(join(project, 'package.json'), 'utf8'))
    await writeFile(
      join(consumer, 'package.json'),
      JSON.stringify({
        private: true,
        type: 'module',
        devDependencies: { typescript: devDependencies.typescript, vite: devDependencies.vite },
      }),
    )
    await exec(
      'npm',
      ['install', '--include=dev', '--ignore-scripts', '--no-audit', '--no-fund', resolve(archive)],
      { cwd: consumer },
    )
    await exec(process.execPath, ['node_modules/typescript/bin/tsc', '-p', 'tsconfig.json'], {
      cwd: consumer,
    })

    // The example server serves only this consumer's bundle in the fresh directory.
    await exec(process.execPath, ['node_modules/vite/bin/vite.js', 'build', '--outDir', join(dir, 'dist')], {
      cwd: consumer,
    })
    await writeFile(join(dir, 'example/index.html'), '<!doctype html><title>WebRTC package consumer</title>')
    const signaling = join(dir, 'web/src/node')
    await mkdir(signaling, { recursive: true })
    await copyFile(join(project, 'src/node/pairing.ts'), join(signaling, 'pairing.ts'))
    const server = processFor(env, process.execPath, [join(dir, 'example/server.mjs')], { env: settings })
    const [url] = await server.waitFor(/http:\/\/127\.0\.0\.1:\d+/)
    const pages = await Promise.all(
      tokens.map(async (token, i) => {
        const page = await browser.newPage()
        await page.goto(url)
        await page.evaluate(
          async ([id, token]) => {
            const { connect } = await import('/link-webrtc.mjs')
            window.peer = connect(id, token)
          },
          ['page-' + i, token],
        )
        return page
      }),
    )
    await Promise.all(
      pages.map((page, i) =>
        page.waitForFunction(
          (remote) => peer.status().route === 'relay' && peer.services().includes(remote),
          'page-' + (1 - i),
        ),
      ),
    )
    const proof = randomUUID()
    assert.deepEqual(await pages[0].evaluate((v) => peer.echo('page-1', v), proof), ['page-1', proof])
    assert.equal(await pages[1].evaluate((v) => peer.callback('page-0', v), proof), 'page-1:' + proof)
  },
)
