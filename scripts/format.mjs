import { execFileSync, spawnSync } from 'node:child_process'
import { lstatSync } from 'node:fs'

const mode = process.argv[2]
if (!['fmt', 'fmt-check'].includes(mode)) throw Error('Use make fmt or make fmt-check')
const write = mode === 'fmt'
const files = execFileSync('git', ['ls-files', '-z', '--', '*.go', '*.ts', '*.mts', '*.mjs'], {
  encoding: 'utf8',
})
  .split('\0')
  .filter((file) => file && lstatSync(file, { throwIfNoEntry: false })?.isFile())
  .map((file) => './' + file)

const go = files.filter((file) => file.endsWith('.go'))
if (go.length) {
  // gofmt -l reports differences on stdout with exit code zero.
  const result = spawnSync('gofmt', [write ? '-w' : '-l', ...go], {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'inherit'],
  })
  if (result.error) throw result.error
  process.stdout.write(result.stdout)
  if (result.status !== 0 || (!write && result.stdout.length)) process.exitCode = 1
}

const scripts = files.filter((file) => !file.endsWith('.go'))
if (scripts.length) {
  const result = spawnSync(
    'node_modules/.bin/prettier',
    [write ? '--write' : '--list-different', ...scripts],
    {
      stdio: 'inherit',
    },
  )
  if (result.error) throw result.error
  if (result.status !== 0) process.exitCode = 1
}
