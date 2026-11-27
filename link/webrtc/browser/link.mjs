import { abortable, attachChannel, delay } from './channel.mjs'

const fatal = new Set(['pairing-denied', 'incompatible-channel'])
const codes = new Set([...fatal, 'channel-closed', 'turn-unavailable', 'invalid-grant', 'signaling-failed'])
async function signalCall(provider, method, args, signal, retry = false) {
  for (;;) {
    signal.throwIfAborted()
    try {
      return await abortable(provider[method](args, signal), signal)
    } catch (error) {
      signal.throwIfAborted()
      if (error.name === 'PairingDenied' || error.code === 'pairing-denied') throw Error('pairing-denied')
      if (!retry) throw Error('signaling-failed')
      await delay(250, signal)
    }
  }
}

async function selectedRoute(pc) {
  let pair = pc.sctp?.transport?.iceTransport?.getSelectedCandidatePair?.()
  if (!pair) {
    const stats = await pc.getStats()
    const entries = [...stats.values()]
    const transport = entries.find((x) => x.type === 'transport' && x.selectedCandidatePairId)
    const candidates = entries.filter(
      (x) => x.type === 'candidate-pair' && x.nominated && x.state === 'succeeded',
    )
    const id =
      stats.get(transport?.selectedCandidatePairId) ??
      entries.find((x) => x.type === 'candidate-pair' && x.selected) ??
      (candidates.length === 1 && candidates[0])
    if (id) pair = { local: stats.get(id.localCandidateId), remote: stats.get(id.remoteCandidateId) }
  }
  const types = [pair?.local, pair?.remote].map((c) => c?.candidateType ?? c?.type)
  return types.includes('relay') ? 'relay' : types.every(Boolean) ? 'direct' : 'unknown'
}

// During an ICE restart an answerer can otherwise run checks against the
// new offer from its old TURN allocation before it has gathered the new one.
// Install the offer's candidates once the fresh answer has been published.
function deferOfferCandidates(desc) {
  const candidates = []
  let media = -1
  const lines = desc.sdp.split(/\r?\n/).filter((line) => {
    if (line.startsWith('m=')) media++
    if (line.startsWith('a=candidate:') || line === 'a=end-of-candidates') {
      candidates.push({
        sdpMLineIndex: media,
        candidate: line.startsWith('a=candidate:') ? line.slice(2) : '',
      })
      return false
    }
    return true
  })
  return { offer: { type: 'offer', sdp: lines.join('\r\n') }, candidates }
}

function serversFor(grant, policy) {
  const { iceServers = [] } = grant
  if (!Array.isArray(iceServers)) throw Error('invalid-grant')
  if (policy === 'relay' && !iceServers.some((s) => [s.urls].flat().some((url) => /^turns?:/i.test(url))))
    throw Error('turn-unavailable')
  return iceServers
}

async function connect({ signaling, sessions }, config, grant, parent, update) {
  const expiry = Date.parse(grant.expiresAt)
  const remaining = expiry - Date.now()
  if (!(remaining > 0)) throw Error('invalid-grant')
  const pc = new RTCPeerConnection({
    iceServers: serversFor(grant, config.iceTransportPolicy),
    iceTransportPolicy: config.iceTransportPolicy,
  })
  const life = new AbortController()
  const stop = () => life.abort(parent.reason)
  parent.addEventListener('abort', stop, { once: true })
  if (parent.aborted) stop()
  const { signal } = life
  let wire, disconnected, statsTimer
  const attached = Promise.withResolvers()
  const timeout = setTimeout(() => life.abort(Error('invalid-grant')), Math.min(remaining, 2147483647))
  function attach(dc) {
    if (
      wire ||
      dc.label !== 'rutis' ||
      dc.protocol !== 'rutis-link/1' ||
      !dc.ordered ||
      dc.maxRetransmits !== null ||
      dc.maxPacketLifeTime !== null
    ) {
      life.abort(Error('incompatible-channel'))
      return
    }
    wire = attachChannel(dc, life)
    attached.resolve(wire)
  }
  pc.ondatachannel = (e) =>
    grant.role === 'answerer' ? attach(e.channel) : life.abort(Error('incompatible-channel'))
  pc.oniceconnectionstatechange = () => {
    const state = pc.iceConnectionState
    if (state === 'failed' || state === 'closed') life.abort(Error('channel-closed'))
    if (state === 'disconnected')
      disconnected ??= setTimeout(() => life.abort(Error('channel-closed')), 10000)
    else {
      clearTimeout(disconnected)
      disconnected = undefined
    }
  }
  async function exchange(g, signal, restart) {
    const fields = { pairId: config.pairId, attemptId: g.attemptId }
    let deferred = []
    const receive = async (type) => {
      for (;;) {
        const desc = await signalCall(
          signaling,
          'receive',
          { ...fields, type, waitMs: 1000 },
          signal,
          restart,
        )
        if (desc !== null && desc !== undefined) {
          if (desc.type !== type || typeof desc.sdp !== 'string' || !desc.sdp) throw Error('invalid-grant')
          let remote = desc
          if (restart && type === 'offer') {
            const { offer, candidates } = deferOfferCandidates(desc)
            remote = offer
            deferred = candidates
          }
          await abortable(pc.setRemoteDescription(remote), signal)
          return
        }
        await delay(50, signal)
      }
    }
    const publish = async (desc) => {
      // On restart Chromium may still report the previous generation's
      // "complete" immediately after setLocalDescription has resolved.
      const gathered = Promise.withResolvers()
      let relayTimer
      const hasCandidates = () =>
        config.iceTransportPolicy === 'relay'
          ? /^a=candidate:.* typ relay(?:\s|$)/im.test(pc.localDescription?.sdp)
          : /^a=candidate:/im.test(pc.localDescription?.sdp)
      const check = () => {
        if (pc.iceGatheringState === 'complete' && (!restart || hasCandidates())) gathered.resolve()
      }
      // Some interfaces can keep gathering after a usable relay is available.
      // The answerer must deliver its SDP before the offerer's ICE checks expire.
      const candidate = (event) => {
        if (event.candidate?.type === 'relay')
          relayTimer ??= setTimeout(
            () => {
              relayTimer = undefined
              if (hasCandidates()) gathered.resolve()
            },
            restart ? 100 : 2000,
          )
      }
      pc.addEventListener('icegatheringstatechange', check)
      pc.addEventListener('icecandidate', candidate)
      try {
        await abortable(pc.setLocalDescription(desc), signal)
        if (pc.iceGatheringState !== 'complete' || (restart && !hasCandidates())) {
          await abortable(gathered.promise, signal)
        }
      } finally {
        clearTimeout(relayTimer)
        pc.removeEventListener('icecandidate', candidate)
        pc.removeEventListener('icegatheringstatechange', check)
      }
      const { type, sdp } = pc.localDescription
      if (config.iceTransportPolicy === 'relay' && !/^a=candidate:.* typ relay(?:\s|$)/im.test(sdp))
        throw Error('turn-unavailable')
      await signalCall(signaling, 'publish', { ...fields, type, sdp }, signal, restart)
    }

    if (g.role === 'offerer') {
      await publish(await pc.createOffer({ iceRestart: restart }))
      await receive('answer')
    } else {
      await receive('offer')
      await publish(await pc.createAnswer())
      for (const candidate of deferred) await abortable(pc.addIceCandidate(candidate), signal)
    }
  }
  async function renew() {
    let previous = grant
    while (previous.refreshAt && !signal.aborted) {
      const refreshAt = Date.parse(previous.refreshAt)
      if (!Number.isFinite(refreshAt)) throw Error('invalid-grant')
      while (Date.now() < refreshAt) await delay(Math.min(refreshAt - Date.now(), 2147483647), signal)
      let next
      let backoff = 250
      while (!next) {
        try {
          next = await signalCall(
            signaling,
            'acquire',
            {
              pairId: config.pairId,
              after: previous.attemptId,
              renewOf: grant.attemptId,
              waitMs: 1000,
            },
            signal,
          )
        } catch (error) {
          if (error.message !== 'signaling-failed') throw error
        }
        // A signaling outage before ICE restart leaves the data channel usable.
        if (!next) {
          await delay(backoff, signal)
          backoff = Math.min(backoff * 2, 3000)
        }
      }
      const remaining = Date.parse(next.expiresAt) - Date.now()
      if (
        typeof next.attemptId !== 'string' ||
        !next.attemptId ||
        next.attemptId === grant.attemptId ||
        next.attemptId === previous.attemptId ||
        next.renewOf !== grant.attemptId ||
        next.role !== grant.role ||
        !(remaining > 0) ||
        !(Date.parse(next.refreshAt) > refreshAt)
      )
        throw Error('invalid-grant')
      pc.setConfiguration({
        ...pc.getConfiguration(),
        iceServers: serversFor(next, config.iceTransportPolicy),
      })
      const deadline = AbortSignal.any([signal, AbortSignal.timeout(Math.min(remaining, 2147483647))])
      await exchange(next, deadline, true)
      previous = next
    }
  }
  try {
    if (grant.role === 'offerer')
      attach(pc.createDataChannel('rutis', { protocol: 'rutis-link/1', ordered: true }))
    await exchange(grant, signal, false)
    await abortable(attached.promise, signal)
    await abortable(wire.opened, signal)
    if (Date.now() >= expiry) throw Error('invalid-grant')
    clearTimeout(timeout)
    const refresh = async () => {
      const route = await selectedRoute(pc).catch(() => 'unknown')
      if (!signal.aborted) update({ phase: 'connected', route, lastError: undefined })
    }
    await wire.serve(sessions, pc.sctp.maxMessageSize, () => {
      update({ phase: 'connected', lastError: undefined })
      void renew().catch((error) => life.abort(error))
      void refresh()
      statsTimer = setInterval(refresh, 500)
    })
  } finally {
    life.abort(Error('channel-closed'))
    clearTimeout(timeout)
    clearTimeout(disconnected)
    clearInterval(statsTimer)
    parent.removeEventListener('abort', stop)
    pc.close()
  }
}

function validate(config) {
  if (
    typeof config.pairId !== 'string' ||
    !config.pairId.trim() ||
    !['all', 'relay'].includes(config.iceTransportPolicy) ||
    typeof config.reconnect !== 'boolean'
  )
    throw Error('invalid WebRTC settings')
}

export function createWebRtcLink({ sessions, signaling, config: initial }) {
  if (!sessions?.open || !signaling?.acquire || !signaling?.receive || !signaling?.publish)
    throw Error('sessions and signaling are required')
  if (
    !initial ||
    typeof initial !== 'object' ||
    Array.isArray(initial) ||
    Object.keys(initial).some(
      (k) => !['pairId', 'iceTransportPolicy', 'reconnect', 'autoStart'].includes(k),
    ) ||
    ('autoStart' in initial && typeof initial.autoStart !== 'boolean')
  )
    throw Error('invalid WebRTC settings')
  let config = {
    pairId: initial.pairId,
    iceTransportPolicy: initial.iceTransportPolicy ?? 'all',
    reconnect: initial.reconnect ?? true,
  }
  validate(config)
  const instance = crypto.randomUUID()
  let revision = 0,
    after = null,
    changed = Promise.withResolvers()
  let lifetime,
    worker,
    commands = Promise.resolve()
  let state = {
    ...config,
    desired: 'stopped',
    phase: 'stopped',
    route: 'unknown',
    retryCount: 0,
    cursor: instance + '/0',
  }
  const update = (patch) => {
    if (Object.entries(patch).every(([key, value]) => state[key] === value)) return
    state = { ...state, ...patch, cursor: instance + '/' + ++revision }
    changed.resolve()
    changed = Promise.withResolvers()
  }
  const serialize = (action) => {
    const result = commands.then(action)
    commands = result.catch(() => {})
    return result
  }
  async function run(signal) {
    let backoff = 500
    while (!signal.aborted) {
      const started = Date.now()
      update({ phase: 'pairing', route: 'unknown', retryAt: undefined })
      try {
        let grant
        while (!grant) {
          grant = await signalCall(
            signaling,
            'acquire',
            { pairId: config.pairId, after, waitMs: 1000 },
            signal,
          )
          if (!grant) await delay(50, signal)
        }
        if (
          typeof grant.attemptId !== 'string' ||
          !grant.attemptId ||
          grant.attemptId === after ||
          !['offerer', 'answerer'].includes(grant.role)
        )
          throw Error('invalid-grant')
        after = grant.attemptId
        update({ phase: 'negotiating' })
        await connect({ signaling, sessions }, config, grant, signal, update)
      } catch (error) {
        if (signal.aborted) return
        const code = codes.has(error.message) ? error.message : 'connection-failed'
        if (fatal.has(code) || !config.reconnect) {
          update({ phase: 'failed', route: 'unknown', lastError: code })
          return
        }
        if (Date.now() - started >= 60000) backoff = 500
        update({
          phase: 'retrying',
          route: 'unknown',
          lastError: code,
          retryCount: state.retryCount + 1,
          retryAt: new Date(Date.now() + backoff).toISOString(),
        })
        await delay(backoff, signal).catch(() => {})
        backoff = Math.min(30000, backoff * 2)
      }
    }
  }
  async function join() {
    lifetime?.abort(Error('stopped'))
    await worker
  }
  function launch() {
    lifetime = new AbortController()
    update({ ...config, desired: 'running', retryCount: 0, lastError: undefined, retryAt: undefined })
    worker = run(lifetime.signal)
  }
  const api = {
    status: () => ({ ...state }),
    async waitForChange(cursor, signal) {
      if (cursor === state.cursor) await (signal ? abortable(changed.promise, signal) : changed.promise)
      return api.status()
    },
    start: () =>
      serialize(async () => {
        if (state.desired !== 'running' || state.phase === 'failed') {
          await join()
          launch()
        }
      }),
    stop: () =>
      serialize(async () => {
        await join()
        update({
          desired: 'stopped',
          phase: 'stopped',
          route: 'unknown',
          retryCount: 0,
          lastError: undefined,
          retryAt: undefined,
        })
      }),
    configure: (patch) =>
      serialize(async () => {
        if (
          !patch ||
          typeof patch !== 'object' ||
          Array.isArray(patch) ||
          Object.keys(patch).some((k) => !Object.hasOwn(config, k))
        )
          throw Error('invalid settings')
        const next = { ...config, ...patch }
        validate(next)
        const pairChanged = next.pairId !== config.pairId
        const replace = pairChanged || next.iceTransportPolicy !== config.iceTransportPolicy
        if (!replace && config.reconnect === next.reconnect) return
        if (replace) await join()
        if (pairChanged) after = null
        config = next
        update(config)
        if (state.desired !== 'running') return
        if (replace || (state.phase === 'failed' && next.reconnect && !fatal.has(state.lastError))) launch()
        else if (state.phase === 'retrying' && !next.reconnect) {
          await join()
          update({ phase: 'failed', retryAt: undefined })
        }
      }),
  }
  if (initial.autoStart !== false) launch()
  return api
}
