import {
  createPageEndpoint,
  createWebRtcLink,
  httpSignaling,
  type LinkConfig,
  type LinkStatus,
  type Signaling,
} from '@arcships/rutis-link-webrtc'

export function connect(id: string, token: string) {
  const endpoint = createPageEndpoint(id)
  endpoint.provide(id, {
    echo: (value: string) => [id, value],
    callback: (fn: (value: string) => Promise<string>, value: string) => fn(value),
  })
  const signaling: Signaling = httpSignaling('/signal', { headers: { Authorization: 'Bearer ' + token } })
  const config: LinkConfig = { pairId: 'desk-and-page', iceTransportPolicy: 'relay' }
  const link = createWebRtcLink({ sessions: endpoint.sessions, signaling, config })
  return {
    status: (): LinkStatus => link.status(),
    services: () => endpoint.remoteServices(),
    echo: (peer: string, value: string) => endpoint.call(peer, 'echo', value),
    callback: (peer: string, value: string) =>
      endpoint.call(peer, 'callback', (v: string) => id + ':' + v, value),
  }
}
