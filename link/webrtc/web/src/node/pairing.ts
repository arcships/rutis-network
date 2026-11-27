import { randomUUID } from 'node:crypto'
import { setTimeout as wait } from 'node:timers/promises'

import type { Description, IceServer } from '../shared/signaling.ts'

interface PairingOptions {
  pairId: string
  members: readonly [string, string]
  iceServers?: (member: string, expires: number, issued: number) => IceServer[] | Promise<IceServer[]>
  lifetimeMs?: number
  refreshAfterMs?: number
}

const denied = () => Object.assign(Error('pairing denied'), { name: 'PairingDenied' })

// Call from an authenticated application route. member is the verified
// identity from that request, never a field from the request body.
export function createPairing({
  pairId,
  members,
  iceServers = () => [],
  lifetimeMs = 60000,
  refreshAfterMs,
}: PairingOptions) {
  if (
    typeof pairId !== 'string' ||
    !pairId.trim() ||
    !Array.isArray(members) ||
    members.length !== 2 ||
    !members.every((x) => typeof x === 'string' && x) ||
    members[0] === members[1] ||
    !Number.isFinite(lifetimeMs) ||
    lifetimeMs <= 0 ||
    (refreshAfterMs !== undefined && (!Number.isSafeInteger(refreshAfterMs) || refreshAfterMs <= 0))
  )
    throw Error('invalid pairing')
  const [offerer, answerer] = members
  type Round = ReturnType<typeof attempt>
  let initial: Round | undefined, active: Round | undefined, renewal: Round | undefined
  const waiting = new Set<string>()
  function attempt(previous?: string | null) {
    const now = Date.now()
    return {
      id: randomUUID(),
      previous,
      issued: now,
      expires: now + lifetimeMs,
      refreshAt: refreshAfterMs && new Date(now + refreshAfterMs).toISOString(),
      descriptions: new Map<Description['type'], string>(),
    }
  }
  return async function handle(
    member: string,
    method: string,
    input: Record<string, unknown>,
    signal?: AbortSignal,
  ) {
    if ((member !== offerer && member !== answerer) || input?.pairId !== pairId) throw denied()
    const role = member === offerer ? 'offerer' : 'answerer'
    if (method === 'acquire') {
      const rotating = input.renewOf !== undefined
      if (rotating) {
        if (!active?.refreshAt || input.renewOf !== active.id) throw Error('inactive session')
        // A repeated poll receives the same round. Only advance when both
        // members have finished the previous SDP exchange and requested more.
        if (typeof input.after !== 'string') throw Error('invalid renewal predecessor')
        const pending = renewal
        const replay = pending !== undefined && input.after === pending.previous
        const undelivered = replay && Date.now() >= pending.expires && pending.descriptions.size === 0
        if (!replay || undelivered) {
          const previous = renewal ?? active
          if (!undelivered && (input.after !== previous.id || previous.descriptions.size !== 2))
            throw Error('invalid renewal predecessor')
          waiting.add(member)
          if (waiting.size !== 2) return null
          renewal = attempt(input.after)
          waiting.clear()
        }
      } else if (!initial || Date.now() >= initial.expires || input.after === initial.id) {
        initial = attempt()
      }
      const current = rotating ? renewal : initial
      if (!current) throw Error('expired attempt')
      const servers = await iceServers(member, current.expires, current.issued)
      if (current !== (rotating ? renewal : initial) || Date.now() >= current.expires)
        throw Error('expired attempt')
      return {
        attemptId: current.id,
        role,
        expiresAt: new Date(current.expires).toISOString(),
        ...(current.refreshAt && { refreshAt: current.refreshAt }),
        ...(rotating && { renewOf: active?.id }),
        iceServers: servers,
      }
    }
    const current = input.attemptId === initial?.id ? initial : renewal
    const valid = () =>
      current &&
      (current === initial || current === renewal) &&
      input.attemptId === current.id &&
      Date.now() < current.expires
    if (!current || !valid()) throw Error('expired attempt')
    const own = role === 'offerer' ? 'offer' : 'answer'
    if (method === 'publish') {
      if (input.type !== own || typeof input.sdp !== 'string' || !input.sdp || input.sdp.length > 262144)
        throw denied()
      const existing = current.descriptions.get(own)
      if (existing !== undefined && existing !== input.sdp)
        throw Error('attempt already has a different description')
      current.descriptions.set(own, input.sdp)
      // An extra tab can start a fresh negotiation while a pair is still
      // connected. Transfer renewal ownership only after both peers publish.
      if (current === initial && current !== active && current.descriptions.size === 2) {
        active = current
        renewal = undefined
        waiting.clear()
      }
      return null
    }
    if (method !== 'receive' || input.type !== (own === 'offer' ? 'answer' : 'offer')) throw denied()
    const until = Date.now() + Math.min(1000, Math.max(0, Number(input.waitMs) || 0))
    const type = input.type as Description['type']
    while (valid() && !current.descriptions.has(type) && Date.now() < until)
      await wait(Math.min(40, until - Date.now()), undefined, { signal })
    if (!current || !valid()) throw Error('expired attempt')
    const sdp = current.descriptions.get(type)
    return sdp === undefined ? null : { type: input.type, sdp }
  }
}
