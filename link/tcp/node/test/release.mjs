import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import test from 'node:test'
import { row, setup, startHost } from './fixtures/hosts.mjs'

test('stock hosts exchange a remote RPC over the installed TCP release', { timeout: 60000 }, async (t) => {
  const install = resolve(process.env.TCP_INSTALL_DIR)
  const config = JSON.parse(await readFile(join(install, 'rutis.json'), 'utf8'))
  const env = await setup(t, { pluginDir: resolve(install, config.runtimes.go.dir) })
  const proof = randomUUID()
  const listener = await startHost(env, 'server', [
    row('listen', 'go:link-tcp-listen', { ...env.identity, port: 0 }),
    row('witness', 'test/fixtures/witness.mjs', { proof }),
  ])
  const port = Number((await listener.waitFor(/link\/tcp listening 127\.0\.0\.1:(\d+)/))[1])
  assert.equal(config.rows[0].name, 'go:link-tcp-dial')
  const [dial] = config.rows
  const client = await startHost(env, 'client', [
    { ...dial, config: { ...dial.config, ...env.identity, port } },
  ])
  await client.call(proof)
})
