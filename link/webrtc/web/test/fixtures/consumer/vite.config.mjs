import { defineConfig } from 'vite'

export default defineConfig({
  build: {
    target: 'es2022',
    lib: { entry: 'app.ts', formats: ['es'], fileName: () => 'link-webrtc.mjs' },
  },
})
