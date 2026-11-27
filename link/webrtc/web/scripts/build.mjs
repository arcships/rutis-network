import { build, context } from 'esbuild'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { copyFile, mkdir, readFile } from 'node:fs/promises'

const require = createRequire(import.meta.url)
const root = fileURLToPath(new URL('../src/browser/', import.meta.url))
const output = fileURLToPath(new URL('../dist/link-webrtc.mjs', import.meta.url))
const manifest = require.resolve('@arcships/rutis-runtime/package.json')
const runtime = dirname(manifest)
const { name, version } = JSON.parse(await readFile(manifest, 'utf8'))
if (version !== '0.23.0') throw Error('Review the browser binding for this rutis release')
const options = {
  entryPoints: [join(root, 'index.mts')],
  outfile: output,
  format: 'esm',
  bundle: true,
  platform: 'browser',
  target: 'es2022',
  legalComments: 'eof',
  inject: [join(root, 'buffer.ts')],
  alias: {
    'rutis-session-v5': join(runtime, 'src/session.mjs'),
    'rutis-codec-v5': join(runtime, 'src/codec.mjs'),
    'node:async_hooks': join(root, 'scope.ts'),
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
}

export async function watchBrowser(onBuild) {
  const watcher = await context({
    ...options,
    write: false,
    plugins: [
      ...options.plugins,
      {
        name: 'serve-browser-build',
        setup(build) {
          build.onEnd((result) => {
            if (result.errors.length === 0) onBuild(result.outputFiles[0].contents)
          })
        },
      },
    ],
  })
  await watcher.watch()
  return watcher
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await build(options)
  const example = new URL('../../dist/', import.meta.url)
  await mkdir(example, { recursive: true })
  await copyFile(output, new URL('link-webrtc.mjs', example))
}
