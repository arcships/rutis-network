import { defineConfig } from 'vitest/config'
import { aliases } from './scripts/browser-runtime.mjs'

export default defineConfig({
  resolve: { alias: aliases },
  test: {
    include: ['test/unit/**/*.test.{ts,mts,mjs}'],
    // Transform the page's session so it uses the same async scope as the browser build.
    server: { deps: { inline: ['@arcships/rutis-runtime'] } },
  },
})
