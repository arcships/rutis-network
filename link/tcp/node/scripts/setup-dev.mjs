// Configure a named loopback peer and both dialers for the two development hosts.
import { execFileSync } from 'node:child_process'
import { access, mkdir, writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const tcp = fileURLToPath(new URL('../../', import.meta.url))
const goConfig = join(tcp, 'rutis.dev.json')
const nodeConfig = join(tcp, 'node/dev/rutis.dev.json')
const peerConfig = join(tcp, '.rutis/peer.json')
for (const path of [goConfig, nodeConfig, peerConfig]) {
  try {
    await access(path)
  } catch (error) {
    if (error.code === 'ENOENT') continue
    throw error
  }
  throw Error('Development configuration already exists: ' + path)
}
const port = Number(process.env.PORT ?? 17443)
if (!Number.isInteger(port) || port < 1 || port > 65535) throw Error('Invalid PORT')
const dir = join(tcp, '.rutis/dev-tls')
await mkdir(dirname(dir), { recursive: true })
await mkdir(dir, { mode: 0o700, recursive: true })
const cert = join(dir, 'localhost.pem'),
  key = join(dir, 'localhost.key')
const opensslConfig = join(dir, 'openssl.cnf')
await writeFile(
  opensslConfig,
  [
    '[req]',
    'distinguished_name=dn',
    'x509_extensions=ext',
    'prompt=no',
    '[dn]',
    'CN=localhost',
    '[ext]',
    'basicConstraints=critical,CA:TRUE',
    'subjectAltName=DNS:localhost,IP:127.0.0.1',
  ].join('\n'),
)
execFileSync(
  'openssl',
  [
    'req',
    '-x509',
    '-newkey',
    'rsa:2048',
    '-nodes',
    '-days',
    '7',
    '-config',
    opensslConfig,
    '-keyout',
    key,
    '-out',
    cert,
  ],
  { stdio: ['ignore', 'ignore', 'inherit'] },
)
const config = { ca: cert, cert, key, port }
async function writeConfig(path, value) {
  await writeFile(path, JSON.stringify({ format: 1, ...value }, null, 2) + '\n', { flag: 'wx' })
}
const binaries = join(tcp, '.rutis/peer-bin')
await mkdir(binaries, { recursive: true })
execFileSync('go', ['build', '-o', join(binaries, 'link-tcp'), './cmd/link-tcp'], {
  cwd: tcp,
  stdio: 'inherit',
})
await writeConfig(peerConfig, {
  id: 'loopback-peer',
  runtimes: { go: { dir: binaries }, node: { project: join(tcp, 'node') } },
  rows: [
    { id: 'accept', name: 'go:link-tcp-listen', config },
    { id: 'echo', name: join(tcp, 'node/test/fixtures/witness.mjs'), config: { proof: 'loopback' } },
  ],
})
await writeConfig(goConfig, {
  rows: [
    { id: 'link-tcp-dial', config },
    { id: 'link-tcp-listen', disabled: true },
    { id: 'link-tcp-attach', disabled: true },
  ],
})
await writeConfig(nodeConfig, { rows: [{ id: 'rutis-network-tcp-reference', config }] })
