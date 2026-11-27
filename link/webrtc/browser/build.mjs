import { build } from 'esbuild'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { readFile } from 'node:fs/promises'

const require = createRequire(import.meta.url)
const root = dirname(fileURLToPath(import.meta.url))
const manifest = require.resolve('@arcships/rutis-runtime/package.json')
const runtime = dirname(manifest)
const { name, version } = JSON.parse(await readFile(manifest, 'utf8'))
if (version !== '0.23.0') throw Error('Review the browser binding for this rutis release')
await build({
  entryPoints: [join(root, 'index.mjs')],
  outfile: join(root, '../dist/link-webrtc.mjs'),
  format: 'esm',
  bundle: true,
  platform: 'browser',
  target: 'es2022',
  legalComments: 'eof',
  inject: [join(root, 'buffer.mjs')],
  alias: {
    'rutis-session-v5': join(runtime, 'src/session.mjs'),
    'rutis-codec-v5': join(runtime, 'src/codec.mjs'),
    'node:async_hooks': join(root, 'scope.mjs'),
  },
  plugins: [
    {
      name: 'rutis-session-platform',
      setup(b) {
        b.onResolve({ filter: /^node:(fs|url|path)$/ }, (args) => ({ path: args.path, namespace: 'page' }))
        b.onLoad({ filter: /.*/, namespace: 'page' }, ({ path }) => {
          // The pinned session reads its implementation name/version once. The
          // runtime's file-loader diagnostics are tree-shaken from this entry.
          if (path === 'node:fs')
            return {
              contents:
                'export const readFileSync = () => ' + JSON.stringify(JSON.stringify({ name, version })),
            }
          if (path === 'node:url')
            return { contents: 'export const fileURLToPath = url => new URL(url).pathname' }
          return {
            contents:
              'export const isAbsolute = path => path.startsWith("/"); export const relative = (cwd, path) => path',
          }
        })
      },
    },
  ],
})
