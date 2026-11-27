import { spawnSync } from 'node:child_process'

const revisions = process.argv.slice(2)
if (revisions.length !== 0 && revisions.length !== 2) {
  throw Error('Usage: node scripts/integration-matrix.mjs [base head]')
}

const shared = [
  '.github/workflows/',
  '.go-version',
  'Makefile',
  'tsconfig.base.json',
  'link/package.json',
  'scripts/in-go-module.sh',
  'scripts/integration-matrix.mjs',
  'scripts/integration-reporter.mjs',
  ':(exclude,glob)**/*.md',
]

function changed(paths) {
  if (revisions.length === 0) return true
  // Include both sides of moves. A diff error must fail the job.
  const diff = spawnSync(
    'git',
    ['diff', '--quiet', '--no-renames', ...revisions, '--', ...shared, ...paths],
    {
      stdio: 'inherit',
    },
  )
  if (diff.error) throw diff.error
  if (diff.status !== 0 && diff.status !== 1) throw Error('Cannot determine integration test scope')
  return diff.status === 1
}

const suites = []
if (changed(['link/tcp/'])) suites.push({ transport: 'tcp', package: 'link/tcp/node' })
if (changed(['group/peer/', 'discovery/mdns/', 'link/tcp/'])) {
  suites.push({ transport: 'peer-group', package: 'link/tcp/node', runner: 'macos-15' })
}
if (
  changed([
    'link/webrtc/',
    ':(glob)link/tcp/*.go',
    ':(exclude,glob)link/tcp/*_test.go',
    'link/tcp/go.mod',
    'link/tcp/go.sum',
    'link/tcp/node/test/fixtures/hosts.mjs',
    'link/tcp/node/test/fixtures/manager.mjs',
    'link/tcp/node/test/fixtures/controller.mjs',
  ])
) {
  suites.push({ transport: 'webrtc', package: 'link/webrtc/web' })
}
console.log(JSON.stringify(suites))
