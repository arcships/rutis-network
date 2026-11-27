import { defineConfig } from 'tsdown'

export default defineConfig({
  entry: ['src/index.ts'],
  platform: 'node',
  target: 'node22.18',
  format: 'esm',
  fixedExtension: false,
  dts: true,
})
