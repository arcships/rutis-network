import type { Signaling } from '../shared/signaling.ts'

// Application-supplied fetch can carry an authenticated same-origin session.
// A provider mounts acquire, publish and receive below the supplied URL.
export function httpSignaling(
  base: string | URL,
  {
    headers = {},
    fetch: request = globalThis.fetch,
  }: { headers?: Record<string, string>; fetch?: typeof fetch } = {},
): Signaling {
  const root = new URL(base, globalThis.location?.href)
  if (root.username || root.password || root.search || root.hash) throw Error('invalid signaling URL')
  if (
    root.protocol !== 'https:' &&
    !(root.protocol === 'http:' && ['localhost', '127.0.0.1', '[::1]'].includes(root.hostname))
  )
    throw Error('signaling requires HTTPS or loopback')
  if (!root.pathname.endsWith('/')) root.pathname += '/'
  async function call(method: keyof Signaling, input: object, signal: AbortSignal) {
    const timeout = AbortSignal.timeout(5000)
    const deadline = signal ? AbortSignal.any([signal, timeout]) : timeout
    const response = await request(new URL(method, root), {
      method: 'POST',
      headers: { ...headers, 'content-type': 'application/json' },
      credentials: 'same-origin',
      redirect: 'error',
      signal: deadline,
      body: JSON.stringify(input),
    })
    if (response.status === 401 || response.status === 403)
      throw Object.assign(Error('pairing denied'), { name: 'PairingDenied' })
    if (!response.ok) throw Error('signaling failed')
    return response.status === 204 ? undefined : response.json()
  }
  return {
    acquire: (input, signal) => call('acquire', input, signal),
    receive: (input, signal) => call('receive', input, signal),
    publish: (input, signal) => call('publish', input, signal),
  }
}
