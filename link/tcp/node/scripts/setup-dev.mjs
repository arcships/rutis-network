// Configure a named loopback peer and both dialers for the two development hosts.
import { execFileSync } from 'node:child_process'
import { createPrivateKey, X509Certificate } from 'node:crypto'
import { link, mkdir, mkdtemp, readFile, rename, rm, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const tcp = fileURLToPath(new URL('../../', import.meta.url))
const goConfig = join(tcp, 'rutis.dev.json')
const nodeConfig = join(tcp, 'node/dev/rutis.dev.json')
const peerConfig = join(tcp, '.rutis/peer.json')
const dir = join(tcp, '.rutis/dev-tls')
const cert = join(dir, 'localhost.pem')
const key = join(dir, 'localhost.key')

async function readIfPresent(path) {
  try {
    return await readFile(path, 'utf8')
  } catch (error) {
    if (error.code !== 'ENOENT') throw error
  }
}

// Publish completed files without overwriting a user's existing configuration.
async function installIfMissing(source, destination) {
  try {
    await link(source, destination)
  } catch (error) {
    if (error.code !== 'EEXIST') throw error
  }
}

async function configuredPort(path, rowId) {
  const json = await readIfPresent(path)
  return json && JSON.parse(json).rows?.find((row) => row.id === rowId)?.config?.port
}

const port = Number(
  (await configuredPort(peerConfig, 'accept')) ??
    (await configuredPort(goConfig, 'link-tcp-dial')) ??
    (await configuredPort(nodeConfig, 'rutis-network-tcp-reference')) ??
    process.env.PORT ??
    17443,
)
if (!Number.isInteger(port) || port < 1 || port > 65535) throw Error('Invalid PORT')
const config = { ca: cert, cert, key, port }
const binaries = join(tcp, '.rutis/peer-bin')
await mkdir(dir, { mode: 0o700, recursive: true })
const staging = await mkdtemp(join(dir, '.setup-'))
try {
  if (!(await readIfPresent(key))) {
    const generated = join(staging, 'key')
    execFileSync('openssl', ['genrsa', '-out', generated, '2048'])
    await installIfMissing(generated, key)
  }
  const pem = await readIfPresent(cert)
  const existing = pem && new X509Certificate(pem)
  if (
    !existing ||
    Date.parse(existing.validTo) <= Date.now() + 86400000 ||
    !existing.checkPrivateKey(createPrivateKey(await readFile(key)))
  ) {
    const opensslConfig = join(staging, 'openssl.cnf')
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
    const generated = join(staging, 'cert')
    execFileSync('openssl', [
      'req',
      '-x509',
      '-key',
      key,
      '-days',
      '7',
      '-config',
      opensslConfig,
      '-out',
      generated,
    ])
    await rename(generated, cert)
  }

  async function writeConfig(filename, destination, value) {
    const file = join(staging, filename)
    await writeFile(file, JSON.stringify({ format: 1, ...value }, null, 2) + '\n')
    await installIfMissing(file, destination)
  }
  await mkdir(binaries, { recursive: true })
  execFileSync('go', ['build', '-o', join(binaries, 'link-tcp'), './cmd/link-tcp'], {
    cwd: tcp,
    stdio: 'inherit',
  })
  await writeConfig('peer.json', peerConfig, {
    id: 'loopback-peer',
    runtimes: { go: { dir: binaries }, node: { project: join(tcp, 'node') } },
    rows: [
      { id: 'accept', name: 'go:link-tcp-listen', config },
      { id: 'echo', name: join(tcp, 'node/test/fixtures/witness.mjs'), config: { proof: 'loopback' } },
    ],
  })
  await writeConfig('go.json', goConfig, {
    rows: [
      { id: 'link-tcp-dial', config },
      { id: 'link-tcp-listen', disabled: true },
      { id: 'link-tcp-attach', disabled: true },
    ],
  })
  await writeConfig('node.json', nodeConfig, { rows: [{ id: 'rutis-network-tcp-reference', config }] })
} finally {
  await rm(staging, { recursive: true, force: true })
}
