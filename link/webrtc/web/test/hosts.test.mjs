import test from 'node:test'
import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { spawn, execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { once } from 'node:events'
import { mkdtemp, mkdir, rm, writeFile, copyFile, symlink } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { setTimeout as delay } from 'node:timers/promises'
import { chromium, firefox } from 'playwright'
import { manager } from '../../../tcp/node/test/fixtures/manager.mjs'

const exec = promisify(execFile)
const project = fileURLToPath(new URL('../', import.meta.url))
const host = join(project, 'node_modules/@arcships/rutis-host/bin/rutis-host.mjs')
const environment = { ...process.env, NODE_NO_WARNINGS: '1' }

async function until(check, detail, timeout = 70000) {
  let last
  const end = Date.now() + timeout
  do {
    try {
      const result = await check()
      if (result) return result
    } catch (error) {
      last = error
    }
    await delay(100)
  } while (Date.now() < end)
  assert.fail('Timed out: ' + (await detail()) + ' ' + (last?.message ?? ''))
}

async function fixture(t) {
  const dir = await mkdtemp(join(tmpdir(), 'rutis-webrtc-'))
  const stops = []
  const logs = []
  t.after(async () => {
    for (const stop of stops.reverse()) await stop()
    await rm(dir, { recursive: true, force: true })
    assert.ok(!logs.some((line) => line.includes('WARNING: DATA RACE')), 'Go race detector reported a race')
  })
  await mkdir(join(dir, 'plugins'))
  await Promise.all([
    exec('go', ['build', '-o', join(dir, 'relay'), './example/turn'], { cwd: join(project, '..') }),
    exec('go', ['build', '-o', join(dir, 'plugins/link-webrtc'), './cmd/link-webrtc'], {
      cwd: join(project, '..'),
    }),
  ])
  const controller = join(dir, 'controller.mjs')
  await copyFile(new URL('../../../tcp/node/test/fixtures/controller.mjs', import.meta.url), controller)
  await symlink(join(project, 'node_modules'), join(dir, 'node_modules'), 'dir')
  function processFor(program, args, env) {
    const child = spawn(program, args, { env, stdio: ['ignore', 'pipe', 'pipe'] })
    const output = []
    for (const stream of [child.stdout, child.stderr])
      stream.on('data', (bytes) => {
        output.push(String(bytes))
        logs.push(String(bytes))
      })
    const ended = once(child, 'exit')
    const stop = async () => {
      if (child.exitCode === null && !child.signalCode) child.kill('SIGTERM')
      await ended
    }
    stops.push(stop)
    return { stop, output: () => output.join('') }
  }
  const turnSecret = randomUUID()
  let port, relay
  async function startRelay() {
    relay = processFor(join(dir, 'relay'), port ? [port] : [], {
      ...environment,
      TURN_SECRET: turnSecret,
    })
    port = await until(() => relay.output().match(/TURN [\d.]+:(\d+)/)?.[1], relay.output)
  }
  await startRelay()
  const tokens = [randomUUID(), randomUUID()]
  const pairings = new Map()
  async function addPair(id, turnTTL = 3600) {
    const relayOnly = id.endsWith('relay')
    const server = processFor(process.execPath, [join(project, '../example/server.mjs')], {
      ...environment,
      PORT: '0',
      PAIR_ID: id,
      PAIR_A_TOKEN: tokens[0],
      PAIR_B_TOKEN: tokens[1],
      STUN_URL: relayOnly ? '' : 'stun:127.0.0.1:' + port,
      TURN_URL: relayOnly ? 'turn:127.0.0.1:' + port + '?transport=udp' : '',
      TURN_SECRET: relayOnly ? turnSecret : '',
      TURN_TTL_SECONDS: String(turnTTL),
    })
    const url = await until(() => server.output().match(/http:\/\/127\.0\.0\.1:\d+/)?.[0], server.output)
    pairings.set(id, url)
    return url
  }
  const chrome = await chromium.launch()
  stops.push(() => chrome.close())
  const ff = await firefox.launch({
    firefoxUserPrefs: {
      // Firefox limits local ICE interfaces without media permission. This
      // ephemeral test profile uses fake devices and never requests a stream.
      'permissions.default.microphone': 1,
      'media.navigator.streams.fake': true,
      'media.peerconnection.ice.loopback': true,
      'media.peerconnection.ice.default_address_only': false,
      'media.peerconnection.ice.obfuscate_host_addresses': false,
      'network.proxy.type': 0,
    },
  })
  stops.push(() => ff.close())
  async function page(browser, name, member, pairId, policy, observeRotation = false) {
    const url = pairings.get(pairId)
    const tab = await browser.newPage()
    tab.on('pageerror', (e) => logs.push(e.stack))
    await tab.goto(url)
    const init = async () =>
      tab.evaluate(
        async ({ name, token, url, pairId, policy, observeRotation }) => {
          const { createPageEndpoint, createWebRtcLink, httpSignaling } = await import('/link-webrtc.mjs')
          const endpoint = (window.endpoint = createPageEndpoint(name))
          endpoint.provide(name, {
            echo: (value) => [name, value],
            callback: (fn, value) => fn(value),
            length: (bytes) => bytes.length,
          })
          const http = httpSignaling(url + '/signal', { headers: { Authorization: 'Bearer ' + token } })
          let sessions = endpoint.sessions,
            signaling = http
          if (observeRotation) {
            window.mounts = 0
            sessions = {
              open(far) {
                window.mounts++
                return endpoint.sessions.open(far)
              },
            }
            let renewal,
              lostReply = false
            signaling = {
              ...http,
              async acquire(input, signal) {
                const grant = await http.acquire(input, signal)
                if (grant && !input.renewOf)
                  window.firstExpiry ??= Number(grant.iceServers[0].username.split(':')[0]) * 1000
                if (grant && input.renewOf) renewal = grant.attemptId
                return grant
              },
              async publish(input, signal) {
                const result = await http.publish(input, signal)
                if (input.attemptId === renewal && !lostReply) {
                  lostReply = true
                  throw Error('lost publication reply')
                }
                return result
              },
            }
            const Peer = window.RTCPeerConnection
            window.RTCPeerConnection = class extends Peer {
              constructor(config) {
                super(config)
                window.peer = this
              }
            }
          }
          window.link = createWebRtcLink({
            sessions,
            signaling,
            config: { pairId, iceTransportPolicy: policy },
          })
        },
        { name, token: tokens[member], url, pairId, policy, observeRotation },
      )
    await init()
    return {
      tab,
      reload: async () => {
        await tab.reload()
        await init()
      },
      status: () => tab.evaluate(() => link.status()),
      call: (service, value) => tab.evaluate(([s, v]) => endpoint.call(s, 'echo', v), [service, value]),
    }
  }
  async function native(member, pairId, policy) {
    const url = pairings.get(pairId)
    const config = join(dir, 'rutis.json')
    const scope = { 'app-pairing': 'private', 'rtc-control': 'private' }
    const management = manager({ dir }, 'ui-' + policy, 'rtc-control', scope)
    const rows = [
      {
        id: 'signaling',
        name: 'go:link-webrtc-signaling',
        isolate: scope,
        config: { service: 'app-pairing', url: url + '/signal', tokenEnv: 'TEST_PAIR_TOKEN' },
      },
      {
        id: 'network',
        name: 'go:link-webrtc',
        isolate: scope,
        inject: ['app-pairing'],
        config: {
          pairId,
          iceTransportPolicy: policy,
          signalingService: 'app-pairing',
          controlService: 'rtc-control',
        },
      },
      { id: 'sensor', name: join(project, 'test/native-service.mjs') },
      { ...management.ui, name: controller },
    ]
    await writeFile(
      config,
      JSON.stringify({
        id: 'device',
        runtimes: { go: { dir: join(dir, 'plugins') }, node: { project } },
        rows,
      }),
    )
    const proc = processFor(process.execPath, [host, 'run', config], {
      ...environment,
      TEST_PAIR_TOKEN: tokens[member],
    })
    const call = async (method, ...args) =>
      JSON.parse(
        (
          await exec(
            process.execPath,
            [host, 'call', '--config', config, method, ...args.map((v) => JSON.stringify(v))],
            { timeout: 4000 },
          )
        ).stdout.trim() || 'null',
      )
    return { call, proc, row: rows[2], manage: management.invoke }
  }
  return {
    page,
    native,
    chrome,
    ff,
    addPair,
    tokens,
    startRelay,
    stopRelay: () => relay.stop(),
    diagnostic: () => logs.slice(-18).join(''),
  }
}

test(
  'real browser and rutis endpoints over direct ICE and authenticated TURN',
  { timeout: 480000 },
  async (t) => {
    const e = await fixture(t)
    for (const policy of ['all', 'relay']) {
      const route = policy === 'all' ? 'direct' : 'relay'
      await t.test('Chromium ↔ Firefox ' + route, async () => {
        const pairId = 'web-' + route
        await e.addPair(pairId)
        const a = await e.page(e.chrome, 'alpha', 1, pairId, policy)
        const b = await e.page(e.ff, 'bravo', 0, pairId, policy)
        const diag = async () => JSON.stringify([await a.status(), await b.status()]) + e.diagnostic()
        try {
          await until(
            async () =>
              (await a.status()).route === route &&
              (await b.status()).route === route &&
              (await a.call('bravo', 17))[1] === 17,
            diag,
          )
          assert.deepEqual(await b.call('alpha', 'ok'), ['alpha', 'ok'])
          assert.equal(await a.tab.evaluate(() => endpoint.call('bravo', 'callback', (n) => n + 2, 40)), 42)
          assert.equal(
            await a.tab.evaluate(() => endpoint.call('bravo', 'length', new Uint8Array(80000))),
            80000,
          )
          if (policy === 'relay') {
            await e.stopRelay()
            await until(
              async () => !(await a.tab.evaluate(() => endpoint.remoteServices())).includes('bravo'),
              diag,
              45000,
            )
            await e.startRelay()
            await until(
              async () => (await a.status()).route === 'relay' && (await a.call('bravo', 39))[1] === 39,
              diag,
              65000,
            )
          } else {
            await b.reload()
            await until(async () => (await a.call('bravo', 31))[1] === 31, diag)
            await a.tab.evaluate(() => link.configure({ iceTransportPolicy: 'relay', reconnect: false }))
            await until(async () => (await a.status()).phase === 'failed', diag)
            assert.equal((await a.status()).lastError, 'turn-unavailable')
          }
        } finally {
          await a.tab.close()
          await b.tab.close()
        }
      })
      await t.test('rutis-host ↔ Chromium ' + route, async () => {
        const pairId = 'native-' + route
        await e.addPair(pairId)
        const nativeMember = policy === 'relay' ? 0 : 1
        const native = await e.native(nativeMember, pairId, policy)
        const web = await e.page(e.chrome, 'page', 1 - nativeMember, pairId, policy)
        const diag = async () => JSON.stringify(await web.status()) + e.diagnostic()
        try {
          await until(
            async () => (await web.status()).route === route && (await web.call('sensor', 21))[1] === 21,
            diag,
          )
          const before = await native.manage('status')
          assert.equal(before.route, route)
          assert.equal(before.pairId, pairId)
          assert.equal(before.desired, 'running')
          assert.equal(before.iceTransportPolicy, policy)
          assert.ok(
            !(await web.tab.evaluate(() => endpoint.remoteServices())).some((s) =>
              ['app-pairing', 'rtc-control'].includes(s),
            ),
          )
          await native.manage('configure', { reconnect: false })
          assert.equal((await native.manage('status')).phase, 'connected')
          await native.manage('configure', { reconnect: true })
          assert.deepEqual(await native.call('page.echo', 42), ['page', 42])
          assert.equal(
            await web.tab.evaluate(() => endpoint.call('sensor', 'callback', (n) => n * 2, 21)),
            42,
          )
          const steady = (await native.manage('status')).cursor
          await native.call('rutis/loader.remove', 'sensor')
          await until(
            async () => !(await web.tab.evaluate(() => endpoint.remoteServices())).includes('sensor'),
            diag,
          )
          await native.call('rutis/loader.create', native.row)
          await until(async () => (await web.call('sensor', 22))[1] === 22, diag)
          assert.equal((await native.manage('status')).cursor, steady)
          if (policy === 'relay') {
            await e.stopRelay()
            await until(
              async () => !(await web.tab.evaluate(() => endpoint.remoteServices())).includes('sensor'),
              diag,
              45000,
            )
            await e.startRelay()
            await until(
              async () => (await web.status()).route === 'relay' && (await web.call('sensor', 23))[1] === 23,
              diag,
              65000,
            )
            await native.manage('configure', { iceTransportPolicy: 'all' })
            await until(
              async () => (await web.status()).route === 'relay' && (await web.call('sensor', 24))[1] === 24,
              diag,
            )
          } else {
            await native.manage('configure', { pairId: 'unrelated-pair' })
            await until(
              async () => (await native.manage('status')).phase === 'failed',
              async () => JSON.stringify(await native.manage('status')) + (await diag()),
              12000,
            )
            const denied = await native.manage('status')
            assert.equal(denied.lastError, 'pairing-denied')
            assert.equal(denied.retryCount, 0)
            await native.manage('configure', { pairId })
            await until(async () => (await web.call('sensor', 25))[1] === 25, diag)
          }
        } finally {
          await web.tab.close()
          await native.proc.stop()
        }
      })
    }

    for (const [other, member] of [
      ['Go', 0],
      ['Go', 1],
      ['Firefox', 0],
      ['Firefox', 1],
    ]) {
      const role = member === 0 ? 'offerer' : 'answerer'
      await t.test('in-flight RPC survives TURN rotation with ' + other + ' ' + role, async () => {
        const pairId = 'rotate-' + other + '-' + role + '-relay'
        await e.addPair(pairId, 16)
        const service = other === 'Firefox' ? 'bravo' : 'sensor'
        const remote =
          other === 'Firefox'
            ? await e.page(e.ff, service, member, pairId, 'relay')
            : await e.native(member, pairId, 'relay')
        const web = await e.page(e.chrome, 'page', 1 - member, pairId, 'relay', true)
        const diag = async () =>
          JSON.stringify([await web.status(), await web.tab.evaluate(() => window.mounts)]) + e.diagnostic()
        async function selected() {
          return web.tab.evaluate(async () => {
            const entries = await peer.getStats()
            const transport = [...entries.values()].find(
              (s) => s.type === 'transport' && s.selectedCandidatePairId,
            )
            const pair = entries.get(transport?.selectedCandidatePairId)
            if (!pair || pair.state !== 'succeeded') return
            const local = entries.get(pair.localCandidateId),
              remote = entries.get(pair.remoteCandidateId)
            if (local.candidateType !== 'relay' || remote.candidateType !== 'relay') return
            return {
              ports: [local.port, remote.port],
              generation: peer.currentLocalDescription?.sdp.match(/^a=ice-ufrag:(.+)$/m)?.[1],
            }
          })
        }
        try {
          await until(async () => (await web.call(service, 1))[1] === 1, diag)
          const first = await until(selected, diag)
          assert.ok(first.generation && first.ports.every(Number.isInteger))
          await web.tab.evaluate((service) => {
            window.hold = Promise.withResolvers()
            window.pending = endpoint.call(
              service,
              'callback',
              async () => {
                window.entered = true
                return hold.promise
              },
              42,
            )
            pending.catch(() => {})
          }, service)
          await until(() => web.tab.evaluate(() => window.entered), diag)
          // Observe the selected relay allocation change, then call after the
          // original credentials expired while the first callback is still held.
          await until(
            async () => {
              if (!(await web.tab.evaluate(() => Date.now() > firstExpiry))) return false
              const next = await selected()
              return (
                next?.generation &&
                next.generation !== first.generation &&
                next.ports.every((port, i) => port !== first.ports[i])
              )
            },
            diag,
            55000,
          )
          assert.deepEqual(await web.call(service, 2), [service, 2])
          assert.equal(
            await web.tab.evaluate(() => {
              hold.resolve(42)
              return pending
            }),
            42,
          )
          assert.equal(await web.tab.evaluate(() => mounts), 1)
          assert.equal((await web.status()).retryCount, 0)
          assert.equal((remote.manage ? await remote.manage('status') : await remote.status()).retryCount, 0)
        } finally {
          await web.tab.close()
          if (remote.proc) await remote.proc.stop()
          else await remote.tab.close()
        }
      })
    }

    await t.test('two users connect and call echo through the relay in the example UI', async () => {
      const id = 'demo-relay'
      const url = await e.addPair(id)
      const pages = await Promise.all([e.chrome.newPage(), e.chrome.newPage()])
      try {
        await Promise.all(
          pages.map(async (page, index) => {
            await page.goto(url)
            await page.getByLabel('Page endpoint ID').fill('page-' + index)
            await page.getByLabel('Pair ID').fill(id)
            await page.getByLabel('Pairing credential').fill(e.tokens[index])
            await page.getByLabel('ICE policy').selectOption('relay')
            await page.getByRole('button', { name: 'Connect', exact: true }).click()
            await page.getByText('connected · relay', { exact: true }).waitFor()
          }),
        )
        for (const [index, page] of pages.entries()) {
          const other = 'page-' + (1 - index)
          await page.getByLabel('Remote service').fill(other)
          await until(
            async () => {
              await page.getByRole('button', { name: 'Call echo' }).click()
              return (await page.locator('#reply').textContent()) === JSON.stringify([other, 'hello'])
            },
            () => page.locator('#reply').textContent(),
          )
        }
      } finally {
        await Promise.all(pages.map((page) => page.close()))
      }
    })
  },
)
