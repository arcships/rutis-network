// The 0.23 Node runtime invalidates only the mounted entry in its ESM cache.
// Give it one watched file containing the dialer and its local imports.
import { build } from 'tsdown'
import config from '../tsdown.config.mjs'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { copyFile, rename } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const project = new URL('../dev/', import.meta.url)
const entry = new URL('plugin.mjs', project)
const temporary = new URL('.plugin.tmp', entry)
const firstBuild = Promise.withResolvers()
const output = new URL('../../.rutis/node-build/', import.meta.url)
const bundler = await build({
  ...config,
  config: false,
  cwd: fileURLToPath(new URL('../', import.meta.url)),
  entry: { plugin: 'src/index.ts' },
  outDir: fileURLToPath(output),
  fixedExtension: true,
  dts: false,
  watch: true,
  async onSuccess() {
    await copyFile(new URL('plugin.mjs', output), temporary)
    await rename(temporary, entry)
    firstBuild.resolve()
  },
})
try {
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
  await bundler.watch.close()
}
