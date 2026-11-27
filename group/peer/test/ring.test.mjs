import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { exec, row, setup, startHost } from '../../../link/tcp/node/test/fixtures/hosts.mjs'
import { manager } from '../../../link/tcp/node/test/fixtures/manager.mjs'

const iface = process.env.MDNS_TEST_INTERFACE
const example = new URL('../example/ring-a.json', import.meta.url)

async function identity(env, name, serial) {
  const key = join(env.dir, name + '.key'),
    cert = join(env.dir, name + '.pem')
  await exec('openssl', [
    'req',
    '-new',
    '-newkey',
    'rsa:2048',
    '-nodes',
    '-subj',
    '/CN=' + name,
    '-keyout',
    key,
    '-out',
    cert + '.csr',
  ])
  await writeFile(
    cert + '.ext',
    'extendedKeyUsage=serverAuth,clientAuth\nsubjectAltName=DNS:' + name + '.example\n',
  )
  await exec('openssl', [
    'x509',
    '-req',
    '-in',
    cert + '.csr',
    '-CA',
    env.identity.ca,
    '-CAkey',
    env.identity.key,
    '-set_serial',
    String(serial),
    '-days',
    '1',
    '-extfile',
    cert + '.ext',
    '-out',
    cert,
  ])
  return { ca: env.identity.ca, cert, key }
}

test(
  'three hosts form the configured mTLS ring from live mDNS offers',
  { skip: !iface, timeout: 90000 },
  async (t) => {
    const env = await setup(t)
    for (const [module, binary] of [
      ['discovery/mdns', 'discover-mdns'],
      ['group/peer', 'peer-group'],
    ]) {
      await exec('go', ['build', '-o', join(env.goDir, binary), './cmd/' + binary], {
        cwd: fileURLToPath(new URL('../../../' + module + '/', import.meta.url)),
      })
    }
    const suffix = randomUUID().slice(0, 8)
    const members = ['a', 'b', 'c'].map((n) => n + suffix)
    const nodes = []
    for (const [i, member] of members.entries()) {
      const {
        rows: [group],
      } = JSON.parse(await readFile(example, 'utf8'))
      const next = members[(i + 1) % members.length]
      const cfg = Object.fromEntries(group.config.map((r) => [r.id, r.config]))
      const credentials = await identity(env, member, i + 2)
      Object.assign(cfg.in, credentials)
      Object.assign(cfg.out, credentials)
      Object.assign(cfg.offer, { id: member, serverName: member + '.example' })
      Object.assign(cfg['group-policy'], { members, local: member })
      Object.assign(cfg['dial-successor'], { id: next, serverName: next + '.example' })
      cfg.announce.interface = iface
      cfg.discovery.interface = iface
      const control = manager(env, 'dial-' + i, 'outgoing', group.isolate)
      const prev = members[(i + 2) % members.length]
      group.isolate.standby = 'management'
      const standby = manager(env, 'standby-' + i, 'standby', group.isolate)
      const dialRow = group.config.find((r) => r.id === 'dial-successor')
      group.config.push(
        { id: 'reverse-out', name: 'go:link-tcp-dial', config: { ...cfg.out, controlService: 'standby' } },
        {
          ...dialRow,
          id: 'dial-predecessor',
          inject: ['neighbors', 'standby'],
          config: { ...dialRow.config, dialer: 'standby', id: prev, serverName: prev + '.example' },
        },
        standby.ui,
      )
      const policy = manager(env, 'plan-' + i, 'neighbors', group.isolate)
      group.config.push(control.ui, policy.ui)
      const host = await startHost(env, member, [group])
      t.after(() => {
        if (!t.passed) t.diagnostic(host.output())
      })
      await host.waitFor(/management socket ready neighbors/)
      nodes.push({ host, control, standby, policy, group })
    }
    async function checkRPC(i, step = 1) {
      const { host } = nodes[i]
      const next = members[(i + step) % members.length]
      const witness = row('witness', 'test/fixtures/witness.mjs', { proof: next })
      witness.name = (step === 1 ? 'out:' : 'reverse-out:') + witness.name
      await host.rootCall('rutis/loader.create', [witness])
      await host.call(next)
      await host.rootCall('rutis/loader.remove', ['witness'])
    }
    for (const [i, { control, standby, policy }] of nodes.entries()) {
      const next = members[(i + 1) % members.length],
        prev = members[(i + 2) % members.length]
      const status = await control.until((s) => s.phase === 'connected')
      assert.equal(status.config.servername, next + '.example')
      assert.deepEqual(
        await policy.invoke('neighbors'),
        [
          { member: prev, dialer: prev },
          { member: next, dialer: members[i] },
        ].sort((a, b) => a.member.localeCompare(b.member)),
      )
      const { candidates } = await policy.invoke('snapshot')
      assert.deepEqual(
        candidates.map((c) => c.description.id),
        [next],
      )
      assert.equal(candidates[0].description.params.port, status.config.port)
      assert.equal((await standby.invoke('status')).phase, 'stopped')
      await checkRPC(i)
    }
    // Removing B's advertisement stops the dial at A. B's browser and C's dial
    // remain live, so the other two edges can still serve RPCs.
    await nodes[1].host.rootCall('rutis/loader.remove', ['offer'])
    await nodes[0].control.until((s) => s.phase === 'stopped' && s.desired === 'stopped')
    await checkRPC(1)
    await checkRPC(2)
    const offerRow = nodes[1].group.config.find((r) => r.id === 'offer')
    await nodes[1].host.rootCall('rutis/loader.create', [offerRow, 'ring'])
    await nodes[0].control.until((s) => s.phase === 'connected')
    await checkRPC(0)
    // Withdrawing B's policy removes B's outbound edge; the other owners keep theirs.
    await nodes[1].host.rootCall('rutis/loader.remove', ['group-policy'])
    await nodes[1].control.until((s) => s.desired === 'stopped' && s.phase === 'stopped')
    await checkRPC(0)
    await checkRPC(2)
    // Roll out the reverse roster. Only the policy rows change; the preconfigured
    // adapters for the other neighbor now become the sole dialers.
    for (const i of [0, 2]) {
      await nodes[i].host.rootCall('rutis/loader.remove', ['group-policy'])
      await nodes[i].control.until((s) => s.phase === 'stopped')
    }
    for (const { host, group } of nodes) {
      const policyRow = group.config.find((r) => r.id === 'group-policy')
      policyRow.config.members = [...members].reverse()
      await host.rootCall('rutis/loader.create', [policyRow, 'ring'])
    }
    for (const [i, { control, standby }] of nodes.entries()) {
      const status = await standby.until((s) => s.phase === 'connected')
      assert.equal(status.config.servername, members[(i + 2) % members.length] + '.example')
      assert.equal((await control.invoke('status')).phase, 'stopped')
      await checkRPC(i, 2)
    }
  },
)
