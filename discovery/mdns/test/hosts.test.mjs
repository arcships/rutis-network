import assert from 'node:assert/strict'
import { once } from 'node:events'
import { randomUUID } from 'node:crypto'
import { readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { connect } from 'node:tls'
import { setTimeout as delay } from 'node:timers/promises'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { exec, row, setup, startHost } from '../../../link/tcp/node/test/fixtures/hosts.mjs'
import { manager } from '../../../link/tcp/node/test/fixtures/manager.mjs'

const iface = process.env.MDNS_TEST_INTERFACE
test(
  'two hosts discover the ephemeral TCP listener, authenticate and call across the session',
  { skip: !iface, timeout: 90000 },
  async (t) => {
    const env = await setup(t)
    await exec('go', ['build', '-o', join(env.goDir, 'discover-mdns'), './cmd/discover-mdns'], {
      cwd: fileURLToPath(new URL('../', import.meta.url)),
    })
    const id = 'test-' + randomUUID()
    const identity = async (side) => {
      const key = join(env.dir, side + '.key'),
        cert = join(env.dir, side + '.pem'),
        csr = cert + '.csr',
        ext = cert + '.ext'
      await exec('openssl', [
        'req',
        '-new',
        '-newkey',
        'rsa:2048',
        '-nodes',
        '-subj',
        '/CN=' + side,
        '-keyout',
        key,
        '-out',
        csr,
      ])
      await writeFile(
        ext,
        side === 'listener'
          ? 'extendedKeyUsage=serverAuth\nsubjectAltName=DNS:office.example\n'
          : 'extendedKeyUsage=clientAuth\n',
      )
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
        side === 'listener' ? '2' : '3',
        '-days',
        '1',
        '-extfile',
        ext,
        '-out',
        cert,
      ])
      return { key, cert, ca: env.identity.ca }
    }
    const manifest = async (side) => {
      const { rows } = JSON.parse(
        await readFile(new URL('../example/' + side + '.json', import.meta.url), 'utf8'),
      )
      for (const entry of rows[0].config) {
        if (entry.config.controlService) Object.assign(entry.config, await identity(side))
        if (entry.config.interface) entry.config.interface = iface
        if (entry.config.id) entry.config.id = id
      }
      return rows
    }
    const listening = await manifest('listener'),
      dialing = await manifest('dialer')
    const probe = manager(env, 'observations', 'lan-offers', dialing[0].isolate)
    const dialControl = manager(env, 'dial-control', 'tcp-dialer', dialing[0].isolate)
    dialing[0].config.push(probe.ui, dialControl.ui)
    const listener = await startHost(env, 'listener', listening)
    const dialer = await startHost(env, 'dialer', dialing)
    t.after(() => {
      if (!t.passed) {
        t.diagnostic(listener.output())
        t.diagnostic(dialer.output())
      }
    })
    await dialer.waitFor(/management socket ready lan-offers/)
    const observe = async (present) => {
      for (let i = 0; i < 100; i++) {
        const { candidates } = await probe.invoke('snapshot')
        const match = candidates.find((c) => c.description?.id === id)
        if (Boolean(match) === present) return match
        await delay(100)
      }
      assert.fail('unexpected discovery state; listener ' + listener.output() + ' dialer ' + dialer.output())
    }
    const found = await observe(true)
    const port = Number((await listener.waitFor(/link\/tcp listening 127\.0\.0\.1:(\d+)/))[1])
    assert.deepEqual(found.description, {
      id,
      kind: 'rutis.tcp',
      version: 1,
      params: { host: '127.0.0.1', port, serverName: 'office.example' },
    })
    assert.notEqual(found.port, port)
    assert.equal(
      (await dialControl.until((s) => s.phase === 'connected')).config.servername,
      'office.example',
    )
    // Mount a witness through the connector's session, then invoke it from the dialer.
    const proof = randomUUID()
    const remote = row('remote-witness', 'test/fixtures/witness.mjs', { proof })
    remote.name = 'wire:' + remote.name
    await dialer.rootCall('rutis/loader.create', [remote])
    await dialer.call(proof)
    const socket = connect({
      host: '127.0.0.1',
      port,
      servername: 'office.example',
      ca: await readFile(env.identity.ca),
      maxVersion: 'TLSv1.2',
    })
    t.after(() => socket.destroy())
    const [err] = await once(socket, 'error')
    assert.match(
      err.message,
      /alert handshake failure|certificate required|certificate rejected|alert bad certificate/i,
    )
    // Keep the listener alive and remove only the source. Discovery withdraws and the managed dialer disconnects.
    await dialer.rootCall('rutis/loader.remove', ['remote-witness'])
    await listener.rootCall('rutis/loader.remove', ['describe-tcp'])
    await observe(false)
    await dialControl.until((s) => s.desired === 'stopped' && s.phase === 'stopped')
  },
)
