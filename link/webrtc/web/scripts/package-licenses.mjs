import { readFile, writeFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'

const require = createRequire(import.meta.url)
const buffer = createRequire(require.resolve('buffer/package.json'))
const notices = [await readFile(new URL('../../../../LICENSE', import.meta.url), 'utf8')]
for (const [name, resolver] of [
  ['@arcships/rutis-runtime', require],
  ['buffer', require],
  ['base64-js', buffer],
  ['ieee754', buffer],
]) {
  const manifest = resolver.resolve(name + '/package.json')
  notices.push(name + '\n\n' + (await readFile(join(dirname(manifest), 'LICENSE'), 'utf8')))
}
await writeFile(new URL('../dist/LICENSES.txt', import.meta.url), notices.join('\n'))
