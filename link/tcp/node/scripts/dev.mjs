// The 0.23 Node runtime invalidates only the mounted entry in its ESM cache.
// Give it one watched file containing the dialer and its local imports.
import { context } from 'esbuild'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { rename, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const project = new URL('../dev/', import.meta.url)
const entry = new URL('plugin.mjs', project)
const temporary = new URL('.plugin.tmp', entry)
const firstBuild = Promise.withResolvers()
const bundler = await context({
  entryPoints: [fileURLToPath(new URL('../src/index.ts', import.meta.url))],
  bundle: true,
  platform: 'node',
  format: 'esm',
  packages: 'external',
  write: false,
  plugins: [
    {
      name: 'write-dev-entry',
      setup(build) {
        build.onEnd(async (result) => {
          if (result.errors.length) return
          await writeFile(temporary, result.outputFiles[0].contents)
          await rename(temporary, entry)
          firstBuild.resolve()
        })
      },
    },
  ],
})
try {
  await bundler.watch()
  await firstBuild.promise
  const binary = fileURLToPath(import.meta.resolve('@arcships/rutis-host/package.json'))
  const host = spawn(
    process.execPath,
    [join(dirname(binary), 'bin/rutis-host.mjs'), 'dev', fileURLToPath(project)],
    { stdio: 'inherit' },
  )
  const stop = () => host.kill('SIGTERM')
  process.once('SIGINT', stop)
  process.once('SIGTERM', stop)
  const [code] = await once(host, 'exit')
  process.off('SIGINT', stop)
  process.off('SIGTERM', stop)
  process.exitCode = code ?? 1
} finally {
  await bundler.dispose()
}
