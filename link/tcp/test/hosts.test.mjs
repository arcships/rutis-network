import test from 'node:test'
import assert from 'node:assert/strict'
import { execFile, spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { randomUUID } from 'node:crypto'
import { rootCertificates } from 'node:tls'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import dial from '../dial.ts'

const exec = promisify(execFile)
const project = fileURLToPath(new URL('../', import.meta.url))
const host = join(project, 'node_modules', '@arcships', 'rutis-host', 'bin', 'rutis-host.mjs')

async function startHost(t, dir, id, plugin, config) {
  await mkdir(join(dir, id))
  const path = join(dir, id, 'rutis.json')
  await writeFile(path, JSON.stringify({
    id, runtimes: { node: { project } },
    rows: [{ id: 'wire', name: join(project, plugin), config }],
  }))
  const child = spawn(process.execPath, [host, 'run', path], { stdio: ['ignore', 'pipe', 'pipe'] })
  const ended = once(child, 'exit')
  let output = ''
  child.stdout.on('data', data => { output += data })
  child.stderr.on('data', data => { output += data })
  t.after(async () => {
    child.kill('SIGTERM')
    await ended
  })
  const waitFor = async pattern => {
    for (let i = 0; i < 200 && child.exitCode === null; i++) {
      const match = output.match(pattern)
      if (match) return match
      await new Promise(resolve => setTimeout(resolve, 50))
    }
    throw new Error('Host did not reach ' + pattern + '\n' + output)
  }
  return { path, waitFor, reload: () => child.kill('SIGUSR1'), output: () => output }
}

test('two 0.21 hosts call a service across the admitted TCP bridge', async t => {
  const dir = await mkdtemp(join(tmpdir(), 'link-hosts-'))
  t.after(() => rm(dir, { recursive: true, force: true }))
  const cert = join(dir, 'cert.pem')
  const key = join(dir, 'key.pem')
  // Disposable loopback peers share this test certificate. Each run has its own CA.
  await exec('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
    '-subj', '/CN=localhost', '-keyout', key, '-out', cert])
  const proof = randomUUID()
  const receiver = await startHost(t, dir, 'harbor', './test/fixtures/listen.mjs', { cert, key, proof })
  const [, port] = await receiver.waitFor(/LINK_LISTENING (\d+)/)
  const wrongCa = join(dir, 'other-ca.pem')
  await writeFile(wrongCa, rootCertificates[0])
  let opened = 0
  const ctx = {
    signal: new AbortController().signal,
    effect() {},
    use() { return { open() { opened++ } } },
  }
  await assert.rejects(dial.apply(ctx, { cert, key, port: Number(port), ca: wrongCa }),
    error => ['DEPTH_ZERO_SELF_SIGNED_CERT', 'SELF_SIGNED_CERT_IN_CHAIN'].includes(error.code))
  assert.equal(opened, 0)

  const sender = await startHost(t, dir, 'coast', './dial.ts', { ca: cert, cert, key, port: Number(port) })
  let last
  for (let i = 0; i < 60; i++) {
    try {
      const status = await exec(process.execPath, [host, 'status', sender.path])
      assert.match(status.stdout, /witness: /)
      const call = await exec(process.execPath, [host, 'call', '--config', sender.path, 'witness.echo', '37'])
      assert.deepEqual(JSON.parse(call.stdout), [proof, 37])
      last = undefined
      break
    } catch (error) {
      last = error
      await new Promise(resolve => setTimeout(resolve, 100))
    }
  }
  if (last) assert.fail([last, sender.output(), receiver.output()].join('\n'))

  // Remove the dial row while both host processes stay up.
  const config = JSON.parse(await readFile(sender.path, 'utf8'))
  config.rows = []
  await writeFile(sender.path, JSON.stringify(config))
  sender.reload()
  await receiver.waitFor(/LINK_CLOSED/)
  assert.match((await exec(process.execPath, [host, 'status', receiver.path])).stdout, /wire: running/)
})
