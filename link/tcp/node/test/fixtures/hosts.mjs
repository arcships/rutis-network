import assert from 'node:assert/strict'
import { execFile, spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdir, mkdtemp, rename, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

export const exec = promisify(execFile)
export const project = fileURLToPath(new URL('../../', import.meta.url))
const host = join(project, 'node_modules', '@arcships', 'rutis-host', 'bin', 'rutis-host.mjs')

export async function setup(t) {
  const dir = await mkdtemp(join(tmpdir(), 'link-hosts-'))
  const stops = []
  t.after(async () => {
    try {
      for (const stop of stops.reverse()) await stop()
    } finally {
      await rm(dir, { recursive: true, force: true })
    }
  })
  const cert = join(dir, 'cert.pem')
  const key = join(dir, 'key.pem')
  // SubjectAltName also lets frpc's Go TLS client verify the loopback relay.
  await writeFile(join(dir, 'cert.cnf'), [
    '[req]', 'distinguished_name = dn', 'x509_extensions = ext', 'prompt = no',
    '[dn]', 'CN = localhost', '[ext]', 'basicConstraints = critical,CA:TRUE',
    'subjectAltName = DNS:localhost,IP:127.0.0.1',
  ].join('\n'))
  await exec('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
    '-config', join(dir, 'cert.cnf'), '-keyout', key, '-out', cert])
  const goDir = join(dir, 'go')
  await mkdir(goDir)
  await exec('go', ['build', '-o', join(goDir, 'link-tcp'), './cmd/link-tcp'], { cwd: join(project, '..') })
  return { dir, stops, goDir, identity: { ca: cert, cert, key } }
}

export function processFor(env, program, args, options = {}) {
  const child = spawn(program, args, { stdio: ['ignore', 'pipe', 'pipe'], ...options })
  let output = ''
  const exited = once(child, 'close')
  // Attach immediately; missing optional binaries must fail the test cleanly.
  exited.catch(() => {})
  child.stdout.on('data', data => { output += data })
  child.stderr.on('data', data => { output += data })
  env.stops.push(async () => {
    child.kill('SIGTERM')
    await exited.catch(() => {})
  })
  async function waitFor(pattern, offset = 0) {
    for (let i = 0; i < 200; i++) {
      const match = output.slice(offset).match(pattern)
      if (match) return match
      if (child.exitCode !== null || child.signalCode || !child.pid) break
      await delay(50)
    }
    throw new Error(`${program} did not reach ${pattern}\n${output}`)
  }
  return { child, waitFor, output: () => output }
}

export function row(id, name, config) {
  return { id, name: name.startsWith('go:') ? name : join(project, name), config }
}

export async function startHost(env, id, rows, options = {}) {
  const directory = join(env.dir, id)
  await mkdir(directory)
  const path = join(directory, 'rutis.json')
  const config = rows => JSON.stringify({ id, runtimes: { go: { dir: env.goDir }, node: { project } }, rows })
  await writeFile(path, config(rows))
  const proc = processFor(env, process.execPath, [host, 'run', path], options)
  async function reload(next) {
    await writeFile(path + '.tmp', config(next))
    await rename(path + '.tmp', path)
    proc.child.kill('SIGUSR1')
  }
  function rootCall(method, args = [], timeout = 30000) {
    return exec(process.execPath, [host, 'call', '--config', path, method, ...args.map(x => JSON.stringify(x))], { timeout })
  }
  async function call(expected, argument = 37) {
    let last
    for (let i = 0; i < 80; i++) {
      try {
        const result = await rootCall('witness.echo', [argument])
        assert.deepEqual(JSON.parse(result.stdout), [expected, argument])
        return
      } catch (error) {
        last = error
        await delay(50)
      }
    }
    assert.fail([last, proc.output()].join('\n'))
  }
  return { ...proc, reload, call, rootCall }
}
