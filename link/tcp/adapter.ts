// Shared by the loopback adapters. Credentials never enter the byte-stream bridge.
import { readFile } from 'node:fs/promises'

export interface Endpoint {
  port: number
  ca: string
  cert: string
  key: string
}

export function endpointSchema(minPort: number, extra: Record<string, object> = {}) {
  return {
    type: 'object', required: ['port', 'ca', 'cert', 'key'],
    properties: {
      port: { type: 'integer', minimum: minPort, maximum: 65535 },
      ca: { type: 'string' }, cert: { type: 'string' }, key: { type: 'string' },
      ...extra,
    },
  }
}

export async function credentials({ ca, cert, key }: Endpoint) {
  const [roots, certificate, privateKey] = await Promise.all([ca, cert, key].map(path => readFile(path)))
  return { ca: roots, cert: certificate, key: privateKey }
}

export function errorCode(error: unknown) {
  const code = (error as NodeJS.ErrnoException | null)?.code
  return typeof code === 'string' ? code : 'connection-failed'
}
