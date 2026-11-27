import test from 'node:test'
import assert from 'node:assert/strict'
import { once } from 'node:events'
import { basename, join } from 'node:path'
import { cp, mkdir, readFile, symlink, writeFile } from 'node:fs/promises'
import { setTimeout as delay } from 'node:timers/promises'
import { availablePort } from './fixtures/sshd.mjs'
import { exec, processFor, project, row, setup, startHost } from './fixtures/hosts.mjs'

const host = join(project, 'node_modules/@arcships/rutis-host/bin/rutis-host.mjs')

async function expectCall(config, method, expected, logs) {
  let detail
  for (let i = 0; i < 80; i++) {
    try {
      const { stdout } = await exec(process.execPath, [host, 'call', '--config', config, method, '37'], {
        timeout: 2000,
      })
      assert.deepEqual(JSON.parse(stdout), [expected, 37])
      return
    } catch (error) {
      detail = error.message
    }
    await delay(120)
  }
  assert.fail(detail + '\n' + logs())
}

test('Go and Node source reload and installation of the packed TCP plugin', async (t) => {
  const env = await setup(t)
  const tcp = join(env.dir, 'tcp')
  const node = join(tcp, 'node')
  await cp(join(project, '..'), tcp, {
    recursive: true,
    filter: (path) =>
      !['node_modules', '.rutis', 'dist', 'plugin.mjs', 'rutis.dev.json'].includes(basename(path)),
  })
  await symlink(join(project, 'node_modules'), join(node, 'node_modules'), 'dir')
  const port = await availablePort()
  await exec('npm', ['run', 'dev:setup'], { cwd: node, env: { ...process.env, PORT: String(port) } })
  const primary = processFor(env, process.execPath, [host, 'run', join(tcp, '.rutis/peer.json')])
  await primary.waitFor(/listening 127\.0\.0\.1/)
  const { config } = JSON.parse(await readFile(join(tcp, 'rutis.dev.json'))).rows[0]
  const peer = await startHost(env, 'pack-peer', [
    row('listen', 'go:link-tcp-listen', { ...config, port: 0 }),
    row('witness', 'test/fixtures/witness.mjs', { proof: 'packed' }),
  ])
  const peerPort = Number((await peer.waitFor(/listening 127\.0\.0\.1:(\d+)/))[1])

  const go = processFor(env, process.execPath, [host, 'dev', tcp])
  await go.waitFor(/rutis-host dev: running/)
  await expectCall(tcp, 'witness.echo', 'loopback', go.output)
  const adapter = join(tcp, 'adapter.go')
  const oldGo = await readFile(adapter, 'utf8')
  const connect = '.DialContext(connectCtx, "tcp", dest.address())'
  assert.ok(oldGo.includes(connect))
  await writeFile(
    adapter,
    oldGo.replace(connect, '.DialContext(connectCtx, "tcp", "127.0.0.1:' + peerPort + '")'),
  )
  await expectCall(tcp, 'witness.echo', 'packed', go.output)
  const closed = once(go.child, 'close')
  go.child.kill('SIGTERM')
  await closed

  const client = processFor(env, process.execPath, [join(node, 'scripts/dev.mjs')], { cwd: node })
  await client.waitFor(/rutis-host dev: running/)
  const dev = join(node, 'dev')
  await expectCall(dev, 'witness.echo', 'loopback', client.output)
  // Edit an imported source file and verify the newly selected peer replies.
  const dial = join(node, 'src/dial.ts')
  const oldTS = await readFile(dial, 'utf8')
  assert.ok(oldTS.includes('port: config.port'))
  await writeFile(dial, oldTS.replace('port: config.port', 'port: ' + peerPort))
  await expectCall(dev, 'witness.echo', 'packed', client.output)
  await writeFile(dial, oldTS)
  const consumer = join(env.dir, 'consumer')
  await mkdir(consumer)
  await writeFile(join(consumer, 'package.json'), '{"private":true,"type":"module"}')
  const { stdout } = await exec('npm', ['pack', '--silent', '--pack-destination', consumer], { cwd: node })
  const tarball = stdout.trim().split('\n').at(-1)
  await exec(
    'npm',
    [
      'install',
      '--omit=dev',
      '--no-audit',
      '--prefer-offline',
      './' + tarball,
      '@arcships/rutis-runtime@0.23.0',
    ],
    { cwd: consumer },
  )
  await writeFile(
    join(consumer, 'app.ts'),
    "import dial from 'rutis-network-tcp-reference'\nvoid dial.apply\n",
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
  const path = join(consumer, 'rutis.json')
  await writeFile(
    path,
    JSON.stringify({
      id: 'consumer',
      runtimes: { node: { project: consumer } },
      rows: [{ id: 'wire', name: 'rutis-network-tcp-reference', config: { ...config, port: peerPort } }],
    }),
  )
  const packed = processFor(env, process.execPath, [host, 'run', path], { cwd: consumer })
  await expectCall(path, 'witness.echo', 'packed', packed.output)
})
