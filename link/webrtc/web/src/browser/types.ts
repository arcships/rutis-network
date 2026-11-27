import type { Signaling } from '../shared/signaling.ts'

type MaybeAsync<T> = T | Promise<T>
export type Close = { kind: 'done' | 'replaced' } | { kind: 'rejected'; reason: string }

export interface Far {
  send(data: Uint8Array): MaybeAsync<void>
  close(why: Close): void
}
export interface Near {
  receive(data: Uint8Array): MaybeAsync<void>
  end(why: Close): MaybeAsync<void>
}
export interface Sessions {
  open(far: Far): MaybeAsync<Near>
}
export interface LinkConfig {
  pairId: string
  iceTransportPolicy?: RTCIceTransportPolicy
  reconnect?: boolean
  autoStart?: boolean
}
export type Settings = Required<Omit<LinkConfig, 'autoStart'>>
export interface LinkStatus extends Settings {
  desired: 'stopped' | 'running'
  phase: 'stopped' | 'pairing' | 'negotiating' | 'connected' | 'retrying' | 'failed'
  route: 'unknown' | 'relay' | 'direct'
  retryCount: number
  retryAt?: string
  lastError?: string
  cursor: string
}
export interface LinkOptions {
  sessions: Sessions
  signaling: Signaling
  config: LinkConfig
}

export interface WebRtcLink {
  status(): LinkStatus
  waitForChange(cursor: string, signal?: AbortSignal): Promise<LinkStatus>
  start(): Promise<void>
  stop(): Promise<void>
  configure(patch: Partial<Settings>): Promise<void>
}
