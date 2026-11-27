import assert from 'node:assert/strict'
import { once } from 'node:events'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { createServer } from 'node:net'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { exec, row, setup, startHost } from '../../tcp/node/test/fixtures/hosts.mjs'

test(
  'two independent WSS plugin rows carry RPCs in both directions and reconnect',
  { timeout: 90000 },
  async (t) => {
    const pluginDir = await mkdtemp(join(tmpdir(), 'wss-plugin-'))
    t.after(() => rm(pluginDir, { recursive: true, force: true }))
    const env = await setup(t, { pluginDir })
    await exec('go', ['build', '-o', join(env.goDir, 'link-websocket'), './cmd/link-websocket'], {
      cwd: fileURLToPath(new URL('../', import.meta.url)),
    })
    const cert = join(env.dir, 'server.pem')
    const key = join(env.dir, 'server.key')
    const csr = join(env.dir, 'server.csr')
    const ext = join(env.dir, 'server.ext')
    await writeFile(
      ext,
      'basicConstraints=critical,CA:FALSE\nsubjectAltName=IP:127.0.0.1\nextendedKeyUsage=serverAuth\n',
    )
    await exec('openssl', [
      'req',
      '-new',
      '-newkey',
      'rsa:2048',
      '-nodes',
      '-subj',
      '/CN=wss-test',
      '-keyout',
      key,
      '-out',
      csr,
    ])
    await exec('openssl', [
      'x509',
      '-req',
      '-in',
      csr,
      '-CA',
      env.identity.ca,
      '-CAkey',
      env.identity.key,
      '-set_serial',
      '2',
      '-days',
      '1',
      '-extfile',
      ext,
      '-out',
      cert,
    ])
    const tokenFile = join(env.dir, 'peer.token')
    await writeFile(tokenFile, 'wss-test-pair-secret\n', { mode: 0o600 })
    const reservation = createServer()
    reservation.listen(0, '127.0.0.1')
    await once(reservation, 'listening')
    const address = 'wss://127.0.0.1:' + reservation.address().port + '/rutis'
    await new Promise((resolve) => reservation.close(resolve))
    const listener = {
      id: 'from-a',
      name: 'go:link-websocket-listen',
      config: { address, tokenFile, cert, key },
    }
    const dialer = {
      id: 'to-b',
      name: 'go:link-websocket-dial',
      config: { address, tokenFile, ca: env.identity.ca },
    }
    const witness = (host) => row('witness', 'test/fixtures/witness.mjs', { proof: host })
    const b = await startHost(env, 'b', [listener, witness('b')])
    const a = await startHost(env, 'a', [dialer])
    t.after(() => {
      if (!t.passed) t.diagnostic(a.output() + '\n' + b.output())
    })
    await a.call('b')
    await b.rootCall('rutis/loader.remove', ['witness'])
    await a.rootCall('rutis/loader.create', [witness('a')])
    await b.call('a')
    await b.rootCall('rutis/loader.remove', ['from-a'])
    await assert.rejects(b.rootCall('witness.echo', ['offline']))
    await b.rootCall('rutis/loader.create', [listener])
    await b.call('a')
    // The reference Rust dialer uses the same wire protocol as the Go listener.
    await a.rootCall('rutis/loader.remove', ['to-b'])
    const reference = await startHost(
      env,
      'reference',
      [{ id: 'peer', name: 'rutis-host/dial', config: { address } }, witness('reference')],
      { env: { ...process.env, RUTIS_TOKEN_PEER: 'wss-test-pair-secret', RUTIS_CA: env.identity.ca } },
    )
    t.after(() => {
      if (!t.passed) t.diagnostic(reference.output())
    })
    await b.call('reference')
  },
)
