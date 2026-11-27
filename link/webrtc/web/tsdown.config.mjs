import { copyFile, mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'tsdown'
import { aliases, browserRuntime } from './scripts/browser-runtime.mjs'

export default defineConfig({
  entry: { 'link-webrtc': 'src/browser/index.mts' },
  tsconfig: 'tsconfig.browser.json',
  platform: 'browser',
  target: 'es2022',
  format: 'esm',
  fixedExtension: true,
  dts: false, // tsc emits the public browser declarations with DOM types.
  alias: aliases,
  plugins: [browserRuntime()],
  deps: { onlyBundle: ['@arcships/rutis-runtime', 'buffer', 'base64-js', 'ieee754'], onlyImport: [] },
  inputOptions: {
    transform: {
      inject: { Buffer: [fileURLToPath(new URL('src/browser/buffer.ts', import.meta.url)), 'Buffer'] },
    },
  },
  async onSuccess() {
    const example = new URL('../dist/', import.meta.url)
    await mkdir(example, { recursive: true })
    await copyFile(new URL('dist/link-webrtc.mjs', import.meta.url), new URL('link-webrtc.mjs', example))
  },
})
