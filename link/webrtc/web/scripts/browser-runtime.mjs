import { createRequire } from 'node:module'
import { dirname, join, normalize, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const manifest = require.resolve('@arcships/rutis-runtime/package.json')
const runtime = dirname(manifest)
const { name, version } = require(manifest)
if (version !== '0.23.0') throw Error('Review the browser binding for this rutis release')

export const aliases = {
  'rutis-session-v5': join(runtime, 'src/session.mjs'),
  'rutis-codec-v5': join(runtime, 'src/codec.mjs'),
  'node:async_hooks': fileURLToPath(new URL('../src/browser/scope.ts', import.meta.url)),
}

// The pinned session reads its manifest once. The error formatter also
// references Node path helpers. Apply these bindings only inside that runtime.
const shims = new Map([
  ['node:fs', 'export const readFileSync = () => ' + JSON.stringify(JSON.stringify({ name, version }))],
  ['node:url', 'export const fileURLToPath = url => new URL(url).pathname'],
  ['node:path', 'export const isAbsolute = p => p.startsWith("/"); export const relative = (_, p) => p'],
])
const prefix = '\0rutis-browser:'
export function browserRuntime() {
  return {
    name: 'rutis-browser-runtime',
    enforce: 'pre',
    resolveId(id, importer) {
      if (!importer || !normalize(importer).startsWith(runtime + sep)) return
      if (shims.has(id)) return prefix + id
    },
    load(id) {
      if (id.startsWith(prefix)) return shims.get(id.slice(prefix.length))
    },
  }
}
