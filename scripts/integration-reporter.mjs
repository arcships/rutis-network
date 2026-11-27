import { tap } from 'node:test/reporters'

async function* requireIntegrationTests(events) {
  let registered = 0
  for await (const event of events) {
    if (event.type === 'test:summary') {
      const { file, counts, success } = event.data
      // The runner counts an empty file as a pass in the final summary only.
      if (file) registered += counts.tests
      else if (success && (!registered || registered !== counts.tests || counts.skipped || counts.todo)) {
        console.error(
          `Required integration tests: ${registered} registered, ${counts.skipped} skipped, ${counts.todo} TODO (including empty files, ${counts.tests} counted)`,
        )
        process.exitCode = 1
      }
    }
    yield event
  }
}

export default async function* report(events) {
  yield* tap(requireIntegrationTests(events))
}
