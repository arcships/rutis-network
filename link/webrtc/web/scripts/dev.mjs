import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { build } from 'tsdown'
import { createServer } from 'vite'
import config from '../tsdown.config.mjs'

export async function startDevPage(server, publish) {
  const vite = await createServer({
    configFile: false,
    root: fileURLToPath(new URL('../../example/', import.meta.url)),
    appType: 'custom',
    publicDir: false,
    optimizeDeps: { noDiscovery: true },
    server: { middlewareMode: true, ws: { server }, fs: { allow: [] } },
  })
  const output = new URL('../.rutis/browser/', import.meta.url)
  const ready = Promise.withResolvers()
  try {
    const watcher = await build({
      ...config,
      config: false,
      cwd: fileURLToPath(new URL('../', import.meta.url)),
      outDir: fileURLToPath(output),
      watch: true,
      async onSuccess() {
        publish(await readFile(new URL('link-webrtc.mjs', output)))
        // Vite queues a broadcast without clients and replays it to the first
        // tab. Refresh only pages that are already connected to this server.
        if (vite.ws.clients.size) vite.environments.client.hot.send({ type: 'full-reload' })
        ready.resolve()
      },
    })
    server.once('close', () => Promise.all([watcher.watch.close(), vite.close()]))
    await ready.promise
    return vite.middlewares
  } catch (error) {
    await vite.close()
    throw error
  }
}
